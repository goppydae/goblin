package agentmgr

import (
	"bufio"
	"context"
	"encoding/json"
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

type PythonAgent struct {
	// enabled is the resolved ENABLED metadata. Default true:
	// a runner constructed without discovery (tests, direct use)
	// must not be silently un-startable.
	enabled bool

	id       string
	typ      string
	path     string
	runner   string
	hostname string

	stdout io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd

	mu   sync.RWMutex
	ctrl *lifecycle.Controller

	requires     []string
	wants        []string
	wantedBy     []string
	requiredBy   []string // includedBy
	capabilities []string

	listenSpec string
	listener   net.Listener

	trafficHandler func()
	watchCancel    context.CancelFunc

	cpuLimit string
	memLimit string

	productionMode bool // added

	nextRunID string
	startTime time.Time
	bus       *eventbus.EventBus[*anypb.Any]

	waitOnce sync.Once
	waitErr  error
	// stopping marks an exit as Stop-initiated so the exit watcher does
	// not double-report it (parity with GoAgent, GAPI-DIV-026).
	stopping bool

	// adoptedPid holds a CRIU-restored process (parity with GoAgent,
	// GOBLIN-DIV-018). Not a child of this program, so no exec.Cmd.
	adoptedPid int
	// adoptedEpoch is that process's start epoch, captured at adopt
	// time so Stop can signal it through procsig's guard rather than by
	// bare PID (parity with GoAgent, GAPI-DIV-046).
	adoptedEpoch uint64
	// adoptedWatch expires that claim when the process goes (parity with
	// GoAgent, GAPI-DIV-048). Guarded by mu; joined by Stop and Reset.
	adoptedWatch *adoptedWatch
}

// Pid returns the running agent process id, or false when no process
// is running (parity with GoAgent).
func (a *PythonAgent) Pid() (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		// A CRIU-restored process has no exec.Cmd (parity with GoAgent).
		if a.adoptedPid != 0 {
			return a.adoptedPid, true
		}
		return 0, false
	}
	return a.cmd.Process.Pid, true
}

func NewPythonAgent(
	id, typ, modulePath, runnerPath string,
	reqs, wants, wantedBy, requiredBy []string,
	listenStream string, // Added listenStream parameter
	cpuLimit, memLimit string,
	caps []string,
	globalBus *eventbus.EventBus[*anypb.Any],
	depView lifecycle.DependencyResolver,
	productionMode bool, // added
) *PythonAgent {
	host, _ := os.Hostname()
	a := &PythonAgent{
		id: id, typ: typ, path: modulePath, runner: runnerPath,
		enabled:        true,
		requires:       append([]string(nil), reqs...),
		wants:          append([]string(nil), wants...),
		wantedBy:       append([]string(nil), wantedBy...),
		requiredBy:     append([]string(nil), requiredBy...),
		capabilities:   append([]string(nil), caps...),
		listenSpec:     listenStream,
		cpuLimit:       cpuLimit,
		memLimit:       memLimit,
		nextRunID:      "",
		bus:            globalBus,
		hostname:       host,
		productionMode: productionMode,
	}
	a.ctrl = lifecycle.NewController(id, host, a, a.bus, depView)

	// Eagerly bind the socket if configured (Socket Activation / Supervisor Managed)
	if listenStream != "" {
		if _, err := a.EnsureListener(); err != nil {
			// We can't return error here easily, but we can log or set state to Error via controller later?
			// For now, let's just log to stdout/stderr (discovery logs it)
			// Or better, let Start() handle the error if it persists, but we try to bind now.
			fmt.Fprintf(os.Stderr, "[supervisor] agent %s: failed to eager bind %s: %v\n", id, listenStream, err)
		}
	}

	return a
}

func (a *PythonAgent) ID() string           { return a.id }
func (a *PythonAgent) Type() string         { return a.typ }
func (a *PythonAgent) Lang() string         { return "python" }
func (a *PythonAgent) Requires() []string   { return a.requires }
func (a *PythonAgent) WantedBy() []string   { return a.wantedBy }
func (a *PythonAgent) RequiredBy() []string { return a.requiredBy }
func (a *PythonAgent) Wants() []string      { return a.wants }
func (a *PythonAgent) Dependencies() []string {
	return append(append([]string(nil), a.requires...), a.wants...)
}
func (a *PythonAgent) Controller() *lifecycle.Controller { return a.ctrl }
func (a *PythonAgent) Describe() map[string]string {
	return map[string]string{
		"id":            a.id,
		"type":          a.typ,
		"language":      "python",
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

func (a *PythonAgent) SetRunID(id string) {
	a.mu.Lock()
	a.nextRunID = id
	a.mu.Unlock()
}

func (a *PythonAgent) Uptime() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.startTime.IsZero() {
		return 0
	}
	return time.Since(a.startTime)
}

// EnsureListener creates the listener if it doesn't exist.
func (a *PythonAgent) EnsureListener() (*os.File, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ensureListenerLocked()
}

func (a *PythonAgent) ensureListenerLocked() (*os.File, error) {
	if a.listenSpec == "" {
		return nil, nil // No socket activation
	}

	if a.listener == nil {
		// Detect network: unix vs tcp
		network := "tcp"
		addr := a.listenSpec
		if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "@") {
			network = "unix"
		}
		// Basic port assume tcp
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

func (a *PythonAgent) SetTrafficHandler(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trafficHandler = fn
}

func (a *PythonAgent) Arm() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armLocked()
}

func (a *PythonAgent) armLocked() error {
	if a.watchCancel != nil {
		return nil // Already armed
	}
	if a.trafficHandler == nil {
		return fmt.Errorf("no traffic handler set")
	}

	// Ensure listener exists
	f, err := a.ensureListenerLocked()
	if err != nil {
		return err
	}
	// We need the raw FD for polling. calling File() returns a copy that we own.
	// But ensuring listener locked gives us a copy.
	// We'll close it in the loop logic or pass ownership?
	// The loop will duplicate it or we keep this one open?
	// If we keep it open, we must close it when disarming.
	// Better: loop creates/closes its own copy to avoid leaking?
	// Actually, just pass this 'f' to background and let bg close it.

	ctx, cancel := context.WithCancel(context.Background())
	a.watchCancel = cancel

	go a.watchLoop(ctx, f)
	return nil
}

func (a *PythonAgent) watchLoop(ctx context.Context, f *os.File) {
	// Goroutine terminus: the loop has no caller to return to.
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to close watch fd", logattr.Module("agentmgr"), logattr.AgentID(a.id), logattr.Err(cerr))
		}
	}()

	// Get raw FD
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

		// periodic poll to check context
		n, err := unix.Poll(pollFd, 250) // 250ms
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			fmt.Fprintf(os.Stderr, "[supervisor] watchLoop Poll error: %v\n", err)
			return // Should we retry?
		}

		if n > 0 {
			if pollFd[0].Revents&unix.POLLIN != 0 {
				// Traffic detected!
				// Disarm first to avoid repeated firing
				a.mu.Lock()
				if a.watchCancel != nil {
					a.watchCancel() // Cancel self/others
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

func (a *PythonAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	// Disarm watcher if active
	if a.watchCancel != nil {
		a.watchCancel()
		a.watchCancel = nil
	}
	// Note: We don't wait for watchLoop to exit, but it should exit quickly (250ms)

	// Reset listener if needed? No, listener is persistent.
	// But if we just accepted a connection in watchLoop (we didn't accept, just polled),
	// the connection is still in backlog. The agent will accept it.

	defer a.mu.Unlock()

	if a.cmd != nil && a.cmd.Process != nil {
		return nil
	}

	// Limits are parsed BEFORE anything is allocated or spawned. A limit
	// the kernel cannot be given is a containment failure, not a
	// formatting nit, so the start fails rather than running the agent
	// unbounded; failing here costs nothing, while failing after exec
	// would leave a live child to unwind (GAPI-DIV-049).
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

	// exec.Command, not exec.CommandContext, and the distinction is
	// load-bearing: ctx bounds the *start operation*, not the agent's
	// lifetime. Binding the child to it hands os/exec a watchdog that
	// SIGKILLs the agent the instant that context is done - which is the
	// moment the caller's start call returns (GAPI-DIV-028). The process is
	// owned by Stop, which sends SIGTERM and escalates on its own schedule.
	cmd := exec.Command(
		"python3", "-u", a.runner,
		"--module", a.path,
		"--id", a.id,
		"--type", a.typ,
		"--start",
	)

	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // start with env

	if a.nextRunID != "" {
		cmd.Env = append(cmd.Env, EnvRunID+"="+a.nextRunID)
	}

	if a.productionMode {
		cmd.Env = append(cmd.Env, EnvRejectDummy+"=1")
	}

	if len(extraFiles) > 0 {
		cmd.ExtraFiles = extraFiles
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("LISTEN_FDS=%d", len(extraFiles)),
			"LISTEN_PID=self", // or just ignore
		)
	}
	a.cmd = cmd
	a.startTime = time.Now()

	// The original code used pipes and streamed output.
	// The new code uses os.Stdout/Stderr directly, so streamControl/streamStderr
	// would not receive anything.
	// For now, we'll keep the pipe logic for control messages and direct stderr.
	// This means we need to revert the cmd.Stdout/Stderr assignments and keep the pipe logic.
	// Re-reading the instruction, it seems the intent was to *restore* socket logic,
	// but the provided diff also changes stdout	// a.stderr, err = a.cmd.StderrPipe()
	// if err != nil {
	// 	return err
	// }

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

	// Oneshot behavior: Wait for completion
	if a.typ == "oneshot" {
		rid := a.nextRunID
		a.publishStatusWithRunID("STARTING", "oneshot running", rid)

		// Release lock while waiting for exit to avoid deadlock with stream handlers
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
	// Stop-initiated exits are its business: the watcher stays quiet
	// (parity with GoAgent, GAPI-DIV-026).
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

func (a *PythonAgent) Stop(ctx context.Context) error {
	a.mu.Lock()

	if a.cmd == nil || a.cmd.Process == nil {
		// A CRIU-restored process has no exec.Cmd but is still running
		// (parity with GoAgent, GAPI-DIV-046).
		if a.adoptedPid != 0 {
			return a.stopAdoptedLocked(ctx)
		}
		// Join any watcher that already cleared the claim (GAPI-DIV-048).
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

	// Release lock to allow stream handlers (stdout/stderr/control) to process
	// output/events and acquire RLock without deadlocking.
	a.mu.Unlock()

	select {
	case err := <-done:
		a.mu.Lock()
		a.publishStatusWithRunID("STOPPED", "process exited", rid)
		a.cleanupAfterExit()
		// Re-arm lazy activation
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
		// Re-arm lazy activation
		if a.listenSpec != "" && a.trafficHandler != nil {
			_ = a.armLocked()
		}
		a.mu.Unlock()
		return context.DeadlineExceeded
	}
}

func (a *PythonAgent) cleanupAfterExit() {
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

func (a *PythonAgent) Reload(ctx context.Context) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return fmt.Errorf("agent not running")
	}
	pythonBin := "python"
	if _, err := exec.LookPath(pythonBin); err != nil {
		if _, err3 := exec.LookPath("python3"); err3 == nil {
			pythonBin = "python3"
		}
	}
	cmd := exec.CommandContext(ctx, pythonBin, a.runner,
		"--module", a.path, "--id", a.id, "--type", a.typ, "--reload",
	)
	if a.nextRunID != "" {
		cmd.Env = append(os.Environ(), EnvRunID+"="+a.nextRunID)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	a.publishStatusWithRunID("RUNNING", "reload complete", a.nextRunID)
	return nil
}

func (a *PythonAgent) Reset() {
	a.mu.Lock()
	w := a.dropAdoptedLocked() // joined below, unlocked: it takes a.mu
	a.cleanupAfterExit()
	a.mu.Unlock()
	w.stop()
}

func (a *PythonAgent) streamControl(r io.Reader) {
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
		case "reloaded":
			a.publishStatus("RUNNING", "agent reloaded")
		case "error":
			msg, _ := getString(m, "error")
			if msg == "" {
				msg = "agent error"
			}
			a.publishStatus("FAILED", msg)
		case "heartbeat":
			a.publishHeartbeat()
		default:
			// no control topic publications from agents - by design
		}
	}
}

func (a *PythonAgent) streamStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		a.publishLog("stderr", sc.Text())
	}
}

func (a *PythonAgent) publishStatus(state, message string) {
	if a.bus == nil {
		return
	}
	a.mu.RLock()
	rid := a.nextRunID
	a.mu.RUnlock()
	a.publishStatusWithRunID(state, message, rid)
}

func (a *PythonAgent) publishStatusWithRunID(state, message, rid string) {
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

func (a *PythonAgent) publishHeartbeat() {
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

func (a *PythonAgent) publishLog(stream string, data any) {
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

func getString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	default:
		return "", false
	}
}

// attachCgroup places pid under the agent's cgroup with spec applied.
// Shared by both runners, which contain their children identically.
//
// Failures here are logged, not fatal. By the time a pid exists the
// child is running, and killing a started agent because the host has no
// delegated cgroup2 tree would be a worse outcome than a supervised but
// unbounded process. The failure this does NOT have to cover is an
// unrepresentable limit string: Start rejects that before the process
// exists (GAPI-DIV-049), so reaching here with a positive field means
// the only thing left that can fail is the kernel interface.
func attachCgroup(id string, spec cgroups.ResourceSpec, pid int) {
	if spec.CPU <= 0 && spec.Memory <= 0 {
		return
	}
	cgName := cgroups.AgentCgroup(id)
	path, err := cgroups.Create(cgName, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[supervisor] failed to create cgroup for %s: %v\n", id, err)
		return
	}
	if err := cgroups.Add(path, pid); err != nil {
		fmt.Fprintf(os.Stderr, "[supervisor] failed to add pid to cgroup for %s: %v\n", id, err)
	}
}

// SetEnabled records whether this agent should be started
// automatically. Set from discovery metadata; absent metadata
// means enabled.
func (a *PythonAgent) SetEnabled(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = v
}

// Enabled reports whether this agent is started automatically.
// A disabled agent is still discovered and registered, and can
// still be started explicitly - the systemd model.
func (a *PythonAgent) Enabled() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.enabled
}
