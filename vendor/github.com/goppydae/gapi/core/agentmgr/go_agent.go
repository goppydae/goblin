package agentmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/core/cgroups"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

type GoAgent struct {
	// enabled is the resolved ENABLED metadata. Default true:
	// a runner constructed without discovery (tests, direct use)
	// must not be silently un-startable.
	enabled bool

	id       string
	typ      string
	path     string // Path to binary
	hostname string

	stdout io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd

	// waitOnce/waitErr serialize cmd.Wait between the oneshot path,
	// Stop, and the exit watcher (mirrors PythonAgent).
	waitOnce sync.Once
	waitErr  error
	// stopping marks an exit as Stop-initiated so the exit watcher does
	// not double-report it.
	stopping bool

	// adoptedPid holds a CRIU-restored process (GOBLIN-DIV-018). Such a
	// process is not a child of this program, so there is no exec.Cmd
	// for it; it reparents to the supervisor's subreaper and its exit
	// arrives through the reap loop rather than cmd.Wait.
	adoptedPid int
	// adoptedEpoch is that process's start epoch, captured at adopt
	// time (GAPI-DIV-046). It is the only thing that distinguishes the
	// adopted process from a later occupant of the same PID, so Stop
	// signals through procsig with it rather than by bare PID.
	adoptedEpoch uint64
	// adoptedWatch expires that claim when the process goes, the way
	// cmd.Wait returning does for a spawned child (GAPI-DIV-048).
	// Guarded by mu; joined by Stop, Reset and a re-adopt.
	adoptedWatch *adoptedWatch

	mu   sync.RWMutex
	ctrl *lifecycle.Controller

	requires     []string
	wants        []string
	wantedBy     []string
	requiredBy   []string
	capabilities []string

	listenSpec string
	listener   net.Listener

	trafficHandler func()
	watchCancel    context.CancelFunc

	cpuLimit string
	memLimit string

	nextRunID string
	startTime time.Time
	bus       *eventbus.EventBus[*anypb.Any]
}

func NewGoAgent(
	id, typ, binaryPath string,
	reqs, wants, wantedBy, requiredBy []string,
	listenStream string,
	cpuLimit, memLimit string,
	caps []string,
	globalBus *eventbus.EventBus[*anypb.Any],
	depView lifecycle.DependencyResolver,
) *GoAgent {
	host, _ := os.Hostname()
	a := &GoAgent{
		id: id, typ: typ, path: binaryPath,
		enabled:      true,
		requires:     append([]string(nil), reqs...),
		wants:        append([]string(nil), wants...),
		wantedBy:     append([]string(nil), wantedBy...),
		requiredBy:   append([]string(nil), requiredBy...),
		capabilities: append([]string(nil), caps...),
		listenSpec:   listenStream,
		cpuLimit:     cpuLimit,
		memLimit:     memLimit,
		nextRunID:    "",
		bus:          globalBus,
		hostname:     host,
	}
	a.ctrl = lifecycle.NewController(id, host, a, a.bus, depView)

	// Eager bind
	if listenStream != "" {
		if _, err := a.EnsureListener(); err != nil {
			fmt.Fprintf(os.Stderr, "[supervisor] agent %s: failed to eager bind %s: %v\n", id, listenStream, err)
		}
	}

	return a
}

func (a *GoAgent) ID() string           { return a.id }
func (a *GoAgent) Type() string         { return a.typ }
func (a *GoAgent) Lang() string         { return "go" }
func (a *GoAgent) Requires() []string   { return a.requires }
func (a *GoAgent) WantedBy() []string   { return a.wantedBy }
func (a *GoAgent) RequiredBy() []string { return a.requiredBy }
func (a *GoAgent) Wants() []string      { return a.wants }
func (a *GoAgent) Dependencies() []string {
	return append(append([]string(nil), a.requires...), a.wants...)
}
func (a *GoAgent) Controller() *lifecycle.Controller { return a.ctrl }

// Pid returns the running agent process id, or false when no process
// is running. Orchestrators use it to capture the runtime locator
// (start epoch, pid namespace) for gossip.
func (a *GoAgent) Pid() (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		// A CRIU-restored process has no exec.Cmd. Reporting it here is
		// what lets NotifyExited match its eventual reap to this agent.
		if a.adoptedPid != 0 {
			return a.adoptedPid, true
		}
		return 0, false
	}
	return a.cmd.Process.Pid, true
}

// Describe mirrors PythonAgent.Describe key-for-key: the Go/Python describe
// schema is a parity contract (the cross-ADK suite asserts it), so the two
// implementations must emit the same keys - notably "language" (not "lang"),
// "path", and the resource limits.
func (a *GoAgent) Describe() map[string]string {
	return map[string]string{
		"id":            a.id,
		"type":          a.typ,
		"language":      "go",
		"path":          a.path,
		"state":         a.ctrl.State(),
		"requires":      strings.Join(a.requires, ","),
		"wants":         strings.Join(a.wants, ","),
		"wanted_by":     strings.Join(a.wantedBy, ","),
		"required_by":   strings.Join(a.requiredBy, ","),
		"capabilities":  strings.Join(a.capabilities, ","),
		"listen_stream": a.listenSpec,
		"cpu_limit":     a.cpuLimit,
		"mem_limit":     a.memLimit,
		"deps":          strings.Join(a.requires, ","),
	}
}

func (a *GoAgent) SetRunID(id string) {
	a.mu.Lock()
	a.nextRunID = id
	a.mu.Unlock()
}

func (a *GoAgent) Uptime() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.startTime.IsZero() {
		return 0
	}
	return time.Since(a.startTime)
}

// EnsureListener creates the listener if it doesn't exist.
func (a *GoAgent) EnsureListener() (*os.File, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ensureListenerLocked()
}

func (a *GoAgent) ensureListenerLocked() (*os.File, error) {
	if a.listenSpec == "" {
		return nil, nil // No socket activation
	}

	if a.listener == nil {
		network := "tcp"
		addr := a.listenSpec
		if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "@") {
			network = "unix"
		}
		if !strings.Contains(addr, ":") && network == "tcp" {
			addr = ":" + addr
		}

		l, err := net.Listen(network, addr)
		if err != nil {
			return nil, fmt.Errorf("failed to bind socket %s: %w", addr, err)
		}
		a.listener = l
	}

	f, err := a.listener.(interface{ File() (*os.File, error) }).File()
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (a *GoAgent) SetTrafficHandler(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trafficHandler = fn
}

func (a *GoAgent) Arm() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armLocked()
}

func (a *GoAgent) armLocked() error {
	if a.watchCancel != nil {
		return nil
	}
	if a.trafficHandler == nil {
		return fmt.Errorf("no traffic handler set")
	}

	f, err := a.ensureListenerLocked()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.watchCancel = cancel

	go a.watchLoop(ctx, f)
	return nil
}

func (a *GoAgent) watchLoop(ctx context.Context, f *os.File) {
	// Goroutine terminus: the loop has no caller to return to.
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to close watch fd", logattr.Module("agentmgr"), logattr.AgentID(a.id), logattr.Err(cerr))
		}
	}()

	rawConn, err := f.SyscallConn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[supervisor] watchLoop SyscallConn error: %v\n", err)
		return
	}

	var fd int
	_ = rawConn.Control(func(descriptor uintptr) {
		fd = int(descriptor)
	})

	if fd < 0 || fd > math.MaxInt32 {
		fmt.Fprintf(os.Stderr, "[supervisor] watchLoop: fd %d out of int32 range\n", fd)
		return
	}
	pollFd := []unix.PollFd{
		{Fd: int32(fd), Events: unix.POLLIN},
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := unix.Poll(pollFd, 250)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			fmt.Fprintf(os.Stderr, "[supervisor] watchLoop Poll error: %v\n", err)
			return
		}

		if n > 0 {
			if pollFd[0].Revents&unix.POLLIN != 0 {
				a.mu.Lock()
				if a.watchCancel != nil {
					a.watchCancel()
					a.watchCancel = nil
				}
				handler := a.trafficHandler
				a.mu.Unlock()

				if handler != nil {
					handler()
				}
				return
			}
		}
	}
}

func (a *GoAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.watchCancel != nil {
		a.watchCancel()
		a.watchCancel = nil
	}
	defer a.mu.Unlock()

	if a.cmd != nil && a.cmd.Process != nil {
		return nil
	}

	// Parsed before anything is allocated or spawned - see PythonAgent's
	// Start and cgroups.ParseResourceSpec (GAPI-DIV-049).
	limits, lerr := cgroups.ParseResourceSpec(a.cpuLimit, a.memLimit)
	if lerr != nil {
		return fmt.Errorf("agent %s: %w", a.id, lerr)
	}

	var extraFiles []*os.File
	if a.listenSpec != "" {
		socketFile, err := a.ensureListenerLocked()
		if err != nil {
			return err
		}
		if socketFile != nil {
			extraFiles = append(extraFiles, socketFile)
			// Our dup of the listener fd; the child holds its own copy. A
			// close failure only leaks our dup - log it, don't fail Start.
			defer func() {
				if cerr := socketFile.Close(); cerr != nil {
					slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to close listener fd dup", logattr.Module("agentmgr"), logattr.AgentID(a.id), logattr.Err(cerr))
				}
			}()
		}
	}

	// Direct execution of the binary. exec.Command, not
	// exec.CommandContext: ctx bounds the *start operation*, not the
	// agent's lifetime. Binding the child to it hands os/exec a watchdog
	// that SIGKILLs the agent the instant that context is done - which is
	// the moment the caller's start call returns (GAPI-DIV-028). The
	// process is owned by Stop, which sends SIGTERM and escalates.
	cmd := exec.Command(a.path, "--start")

	cmd.Env = os.Environ()
	if a.nextRunID != "" {
		cmd.Env = append(cmd.Env, EnvRunID+"="+a.nextRunID)
	}

	if len(extraFiles) > 0 {
		cmd.ExtraFiles = extraFiles
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("LISTEN_FDS=%d", len(extraFiles)),
			"LISTEN_PID=self",
		)
	}
	a.cmd = cmd
	a.startTime = time.Now()

	var err error
	a.waitOnce = sync.Once{} // Reset for new run
	a.stopping = false
	a.stdout, err = a.cmd.StdoutPipe()
	if err != nil {
		return err
	}

	a.stderr, err = a.cmd.StderrPipe()
	if err != nil {
		return err
	}

	go a.streamControl(a.stdout)
	go a.streamStderr(a.stderr)

	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("cmd.Start: %w", err)
	}

	attachCgroup(a.id, limits, a.cmd.Process.Pid)

	// Oneshot behavior: wait for completion. Mirrors PythonAgent.Start -
	// the lock is released around Wait so the stream handlers (which take
	// RLock via publishStatus) cannot deadlock against us, and the lock-free
	// publishStatusWithRunID is used while the lock is held.
	if a.typ == "oneshot" {
		rid := a.nextRunID
		a.publishStatusWithRunID("STARTING", "oneshot running", rid)

		a.mu.Unlock()
		err := a.cmd.Wait()
		a.mu.Lock()

		if err != nil {
			a.publishStatusWithRunID("FAILED", fmt.Sprintf("oneshot failed: %v", err), rid)
			a.cleanupAfterExit()
			return fmt.Errorf("oneshot agent failed: %w", err)
		}
		a.publishStatusWithRunID("COMPLETED", "oneshot success", rid)
		a.cleanupAfterExit()
		return nil
	}

	// Service agents: watch for an exit the supervisor did not initiate
	// (external kill, crash, cluster-delivered signal). A silently dead
	// process must never keep a live state - report FAILED and reap.
	// Stop-initiated exits are its business: the watcher stays quiet.
	watchRunID := a.nextRunID
	watchCmd := a.cmd
	go func() {
		a.waitOnce.Do(func() {
			a.waitErr = watchCmd.Wait()
		})
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.stopping || a.cmd != watchCmd {
			return // Stop owns this exit, or a new run replaced the slot
		}
		// Named explicitly rather than left to the status payload: when
		// this fires the only downstream evidence is a later verb
		// failing with "agent not running", which says nothing about
		// the death that caused it (GAPI-DIV-025).
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent process exited unexpectedly",
			logattr.Module("agentmgr"), logattr.AgentID(a.id),
			slog.Int("pid", watchCmd.Process.Pid),
			slog.Duration("uptime", time.Since(a.startTime)),
			logattr.Err(a.waitErr))
		a.publishStatusWithRunID("FAILED", fmt.Sprintf("process exited unexpectedly: %v", a.waitErr), watchRunID)
		a.cleanupAfterExit()
	}()

	return nil
}

func (a *GoAgent) Stop(ctx context.Context) error {
	a.mu.Lock()

	if a.cmd == nil || a.cmd.Process == nil {
		// A CRIU-restored process has no exec.Cmd, but it is running:
		// returning nil here told the caller "stopped" about a live
		// process (GAPI-DIV-046).
		if a.adoptedPid != 0 {
			return a.stopAdoptedLocked(ctx)
		}
		// No claim, so the exit watcher may have already cleared it.
		// Join it (nil-safe) so Stop never leaves one behind.
		w := a.takeAdoptedWatchLocked()
		a.mu.Unlock()
		w.stop()
		return nil
	}

	rid := a.nextRunID
	a.stopping = true
	_ = a.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() {
		a.waitOnce.Do(func() {
			a.waitErr = a.cmd.Wait()
		})
		done <- a.waitErr
	}()

	// Release the lock while waiting so the stream handlers (which take
	// RLock via publishStatus) can drain output; holding it here was a
	// guaranteed self-deadlock at the publishStatus below (mirrors
	// PythonAgent.Stop).
	a.mu.Unlock()

	select {
	case err := <-done:
		a.mu.Lock()
		a.publishStatusWithRunID("STOPPED", "process exited", rid)
		a.cleanupAfterExit()
		if a.listenSpec != "" && a.trafficHandler != nil {
			_ = a.armLocked()
		}
		a.mu.Unlock()
		return ignoreStopSignalExit(err)
	case <-ctx.Done():
		_ = a.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(750 * time.Millisecond):
		}
		a.mu.Lock()
		a.publishStatusWithRunID("STOPPED", "killed after timeout", rid)
		a.cleanupAfterExit()
		if a.listenSpec != "" && a.trafficHandler != nil {
			_ = a.armLocked()
		}
		a.mu.Unlock()
		return context.DeadlineExceeded
	}
}

// ignoreStopSignalExit maps a signal-caused child exit to success at Stop
// time: whether the signal was Stop's own SIGTERM/SIGKILL or an earlier one
// (e.g. Reload's SIGHUP on an agent that does not handle it), the process
// is stopped - which is Stop's contract. Any non-signal error passes through.
func ignoreStopSignalExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return nil
		}
	}
	return err
}

func (a *GoAgent) cleanupAfterExit() {
	if a.cpuLimit != "" || a.memLimit != "" {
		cgName := cgroups.AgentCgroup(a.id)
		_ = cgroups.Cleanup(cgName)
	}
	if a.stdout != nil {
		_ = a.stdout.Close()
		a.stdout = nil
	}
	if a.stderr != nil {
		_ = a.stderr.Close()
		a.stderr = nil
	}
	a.cmd = nil
	a.startTime = time.Time{}
}

func (a *GoAgent) Reload(ctx context.Context) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return fmt.Errorf("agent not running")
	}

	// Just send SIGHUP for now, or exec new binary?
	// Standard for compiled daemons is often SIGHUP.
	// But our lifecycle assumes full replacement or re-execution.
	// Let's assume standard graceful reload via SIGHUP for now,
	// or full restart if the binary differs?
	// We'll mimic Python's "Call with --reload", but since it's already running...
	// We could run a separate "reloader" process?
	// Or just do a Restart().
	// For Go Service Agents, normally we expect SIGHUP support.
	// But `gapid` manages the process.
	// Let's implement as Stop+Start for now (Restart) unless SIGHUP is preferred.
	// User guide says Reload triggers `ActionReload`.
	// We'll send SIGUSR1 or something?
	// Let's stick to Restart semantics via Stop/Start for safety if binary changed?
	// Actually, `lifecycle` has separate Restart.
	// We'll trust the agent handles `SIGHUP` if we send it.
	_ = a.cmd.Process.Signal(syscall.SIGHUP)
	return nil
}

func (a *GoAgent) Reset() {
	a.mu.Lock()
	w := a.dropAdoptedLocked() // joined below, unlocked: it takes a.mu
	a.cleanupAfterExit()
	a.mu.Unlock()
	w.stop()
}

func (a *GoAgent) streamControl(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			a.publishLog("stdout", line)
			continue
		}
		a.publishLog("stdout", m)

		ev, _ := getString(m, "event")
		switch ev {
		case "starting":
			a.publishStatus("PENDING", "agent starting")
		case "start_pending":
			a.publishStatus("PENDING", "awaiting readiness")
		case "ready":
			a.publishStatus("RUNNING", "agent reported ready")
		case "stopping":
			a.publishStatus("PENDING", "agent stopping")
		case "stopped":
			a.publishStatus("STOPPED", "agent stopped")
		case "error":
			msg, _ := getString(m, "error")
			if msg == "" {
				msg = "agent error"
			}
			a.publishStatus("FAILED", msg)
		case "heartbeat":
			a.publishHeartbeat()
		default:
		}
	}
}

func (a *GoAgent) streamStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		a.publishLog("stderr", sc.Text())
	}
}

// publishStatus reads the run id under the lock; it must NOT be called with
// a.mu held - lock-holding callers use publishStatusWithRunID.
func (a *GoAgent) publishStatus(state, message string) {
	a.mu.RLock()
	rid := a.nextRunID
	a.mu.RUnlock()
	a.publishStatusWithRunID(state, message, rid)
}

// publishStatusWithRunID is lock-free and safe to call with a.mu held
// (mirrors PythonAgent).
func (a *GoAgent) publishStatusWithRunID(state, message, rid string) {
	if a.bus == nil {
		return
	}

	st := &protopkg.LifecycleStatus{
		AgentId:  a.id,
		State:    strings.ToUpper(state),
		Message:  message,
		Time:     timestamppb.Now(),
		Hostname: a.hostname,
		RunId:    rid,
	}
	anyp, _ := anypb.New(st)
	ev := eventbus.NewEvent[*anypb.Any]("system", "", eventbus.TopicAgentLifecycleStatus, a.id, anyp, true)
	_ = a.bus.Publish(ev)
}

func (a *GoAgent) publishHeartbeat() {
	if a.bus == nil {
		return
	}
	st := &protopkg.LifecycleStatus{
		AgentId:  a.id,
		State:    "RUNNING",
		Time:     timestamppb.Now(),
		Hostname: a.hostname,
	}
	anyp, _ := anypb.New(st)
	hb := eventbus.NewEvent[*anypb.Any]("system", "", "agent/heartbeat", a.id, anyp, false)
	_ = a.bus.Publish(hb)
}

func (a *GoAgent) publishLog(stream string, data any) {
	if a.bus == nil {
		return
	}

	// Convert data to string message
	var message string
	switch v := data.(type) {
	case string:
		message = v
	case map[string]any:
		// JSON marshal for structured logs
		if b, err := json.Marshal(v); err == nil {
			message = string(b)
		} else {
			message = fmt.Sprintf("%v", v)
		}
	default:
		message = fmt.Sprintf("%v", v)
	}

	// Map stream to log level
	level := "INFO"
	if stream == "stderr" {
		level = "ERROR"
	}

	logMsg := &protopkg.LogMessage{
		AgentId:   a.id,
		Level:     level,
		Message:   message,
		Timestamp: time.Now().UnixMilli(),
	}

	anyp, _ := anypb.New(logMsg)
	ev := eventbus.NewEvent[*anypb.Any]("system", "", "logs", a.id, anyp, false)
	_ = a.bus.Publish(ev)
}

// SetEnabled records whether this agent should be started
// automatically. Set from discovery metadata; absent metadata
// means enabled.
func (a *GoAgent) SetEnabled(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = v
}

// Enabled reports whether this agent is started automatically.
// A disabled agent is still discovered and registered, and can
// still be started explicitly - the systemd model.
func (a *GoAgent) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}
