package agentmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

type GoAgent struct {
	id       string
	typ      string
	path     string // Path to binary
	hostname string

	stdout io.ReadCloser
	stderr io.ReadCloser
	cmd    *exec.Cmd

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
			fmt.Fprintf(os.Stderr, "[gapi] agent %s: failed to eager bind %s: %v\n", id, listenStream, err)
		}
	}

	return a
}

func (a *GoAgent) ID() string         { return a.id }
func (a *GoAgent) Type() string       { return a.typ }
func (a *GoAgent) Lang() string       { return "go" }
func (a *GoAgent) Requires() []string { return a.requires }
func (a *GoAgent) Wants() []string    { return a.wants }
func (a *GoAgent) Dependencies() []string {
	return append(append([]string(nil), a.requires...), a.wants...)
}
func (a *GoAgent) Controller() *lifecycle.Controller { return a.ctrl }
func (a *GoAgent) Describe() map[string]string {
	return map[string]string{
		"id":            a.id,
		"type":          a.typ,
		"lang":          "go",
		"state":         a.ctrl.State(),
		"requires":      strings.Join(a.requires, ","),
		"wants":         strings.Join(a.wants, ","),
		"wanted_by":     strings.Join(a.wantedBy, ","),
		"required_by":   strings.Join(a.requiredBy, ","),
		"capabilities":  strings.Join(a.capabilities, ","),
		"listen_stream": a.listenSpec,
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
	defer f.Close()

	rawConn, err := f.SyscallConn()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[gapid] watchLoop SyscallConn error: %v\n", err)
		return
	}

	var fd int
	_ = rawConn.Control(func(descriptor uintptr) {
		fd = int(descriptor)
	})

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
			fmt.Fprintf(os.Stderr, "[gapid] watchLoop Poll error: %v\n", err)
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

	var extraFiles []*os.File
	if a.listenSpec != "" {
		socketFile, err := a.ensureListenerLocked()
		if err != nil {
			return err
		}
		if socketFile != nil {
			extraFiles = append(extraFiles, socketFile)
			defer socketFile.Close()
		}
	}

	// Direct execution of the binary
	cmd := exec.CommandContext(ctx, a.path, "--start")

	cmd.Env = os.Environ()
	if a.nextRunID != "" {
		cmd.Env = append(cmd.Env, "GAPI_RUN_ID="+a.nextRunID)
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

	// Apply Resource Limits
	if a.cpuLimit != "" || a.memLimit != "" {
		spec := parseLimits(a.cpuLimit, a.memLimit)
		cgName := fmt.Sprintf("gapid-%s", a.id)
		path, err := cgroups.Create(cgName, spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[gapid] failed to create cgroup for %s: %v\n", a.id, err)
		} else {
			if err := cgroups.Add(path, a.cmd.Process.Pid); err != nil {
				fmt.Fprintf(os.Stderr, "[gapid] failed to add pid to cgroup for %s: %v\n", a.id, err)
			}
		}
	}

	if a.typ == "oneshot" {
		a.publishStatus("STARTING", "oneshot running")
		err := a.cmd.Wait()
		if err != nil {
			a.publishStatus("FAILED", fmt.Sprintf("oneshot failed: %v", err))
			a.cleanupAfterExit()
			return fmt.Errorf("oneshot agent failed: %w", err)
		}
		a.publishStatus("COMPLETED", "oneshot success")
		a.cleanupAfterExit()
		return nil
	}

	return nil
}

func (a *GoAgent) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.cmd == nil || a.cmd.Process == nil {
		return nil
	}

	_ = a.cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()

	select {
	case err := <-done:
		a.publishStatus("STOPPED", "process exited")
		a.cleanupAfterExit()
		if a.listenSpec != "" && a.trafficHandler != nil {
			_ = a.armLocked()
		}
		return err
	case <-ctx.Done():
		_ = a.cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(750 * time.Millisecond):
		}
		a.publishStatus("STOPPED", "killed after timeout")
		a.cleanupAfterExit()
		if a.listenSpec != "" && a.trafficHandler != nil {
			_ = a.armLocked()
		}
		return context.DeadlineExceeded
	}
}

func (a *GoAgent) cleanupAfterExit() {
	if a.cpuLimit != "" || a.memLimit != "" {
		cgName := fmt.Sprintf("gapid-%s", a.id)
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
	defer a.mu.Unlock()
	a.cleanupAfterExit()
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

func (a *GoAgent) publishStatus(state, message string) {
	if a.bus == nil {
		return
	}
	a.mu.RLock()
	rid := a.nextRunID
	a.mu.RUnlock()

	if rid != "" && !strings.Contains(message, "run_id=") {
		if message == "" {
			message = "run_id=" + rid
		} else {
			message += " run_id=" + rid
		}
	}
	st := &protopkg.LifecycleStatus{
		AgentId:  a.id,
		State:    strings.ToUpper(state),
		Message:  message,
		Time:     timestamppb.Now(),
		Hostname: a.hostname,
		RunId:    rid, // structural run id; the run_id= text above is kept for back-compat
	}
	anyp, _ := anypb.New(st)
	ev := eventbus.NewEvent[*anypb.Any]("system", "", "agent/lifecycle.status", a.id, anyp, true)
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
