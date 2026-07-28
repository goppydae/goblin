package agentmgr

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// TimerAgent executes a Python agent on a schedule
type TimerAgent struct {
	id       string
	path     string
	schedule string // systemd-style: OnBootSec=5s, OnUnitActiveSec=30s, etc.
	pyRunner string
	hostname string

	ctrl *lifecycle.Controller
	// lbus is where the controller listens for lifecycle status; the timer
	// must publish its RUNNING/STOPPED transitions there (with the run id
	// the controller set) or ActionStart times out waiting for them - the
	// contract GoAgent/PythonAgent satisfy via publishStatus.
	lbus      *lifecycle.TypedBus
	nextRunID string
	cancel    context.CancelFunc
	mu        sync.Mutex
}

func NewTimerAgent(id, path, schedule, pyRunner string, bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus) *TimerAgent {
	host, _ := os.Hostname()
	ta := &TimerAgent{
		id:       id,
		path:     path,
		schedule: schedule,
		pyRunner: pyRunner,
		hostname: host,
		lbus:     lbus,
	}

	// Create controller with proper signature
	ta.ctrl = lifecycle.NewController(id, host, ta, lbus, nil)

	return ta
}

func (ta *TimerAgent) ID() string             { return ta.id }
func (ta *TimerAgent) Type() string           { return "timer" }
func (ta *TimerAgent) Lang() string           { return "python" }
func (ta *TimerAgent) Dependencies() []string { return nil }
func (ta *TimerAgent) Requires() []string     { return nil }
func (ta *TimerAgent) Wants() []string        { return nil }
func (ta *TimerAgent) SetRunID(id string) {
	ta.mu.Lock()
	ta.nextRunID = id
	ta.mu.Unlock()
}
func (ta *TimerAgent) Controller() *lifecycle.Controller { return ta.ctrl }

func (ta *TimerAgent) Describe() map[string]string {
	return map[string]string{
		"id":       ta.id,
		"type":     "timer",
		"language": "python",
		"path":     ta.path,
		"schedule": ta.schedule,
	}
}

// Implement lifecycle.Runner interface
func (ta *TimerAgent) Initialize(ctx context.Context) error {
	return nil // No initialization needed
}

// Start satisfies lifecycle.Runner. The caller's context is deliberately
// unused: the ticker loop must outlive Start (it is cancelled by Stop),
// so it runs on its own detached context.
func (ta *TimerAgent) Start(_ context.Context) error {
	ta.mu.Lock()
	defer ta.mu.Unlock()

	if ta.cancel != nil {
		return fmt.Errorf("timer already running")
	}

	// Parse schedule (supports systemd-style and cron)
	schedule, err := ParseSchedule(ta.schedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", ta.schedule, err)
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "timer agent started", logattr.AgentID(ta.id), slog.String("schedule", ta.schedule))

	// Start ticker goroutine
	runCtx, cancel := context.WithCancel(context.Background())
	ta.cancel = cancel
	go ta.run(runCtx, schedule)

	// The controller awaits a RUNNING status carrying the run id it set;
	// the ticker loop is our running state.
	ta.publishStatusWithRunID("RUNNING", "timer scheduled", ta.nextRunID)

	return nil
}

func (ta *TimerAgent) Stop(ctx context.Context) error {
	ta.mu.Lock()
	defer ta.mu.Unlock()

	if ta.cancel == nil {
		return nil // already stopped
	}

	ta.cancel()
	ta.cancel = nil

	ta.publishStatusWithRunID("STOPPED", "timer stopped", ta.nextRunID)

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "timer agent stopped", logattr.AgentID(ta.id))
	return nil
}

// publishStatusWithRunID is lock-free and safe to call with ta.mu held
// (the Go/Python agents' status contract).
func (ta *TimerAgent) publishStatusWithRunID(state, message, rid string) {
	if ta.lbus == nil {
		return
	}
	st := &protopkg.LifecycleStatus{
		AgentId:  ta.id,
		State:    state,
		Message:  message,
		Time:     timestamppb.Now(),
		Hostname: ta.hostname,
		RunId:    rid,
	}
	anyp, err := anypb.New(st)
	if err != nil {
		return
	}
	ev := eventbus.NewEvent[*anypb.Any]("system", "", eventbus.TopicAgentLifecycleStatus, ta.id, anyp, true)
	_ = ta.lbus.Publish(ev)
}

func (ta *TimerAgent) Reload(ctx context.Context) error {
	// Stop and restart
	if err := ta.Stop(ctx); err != nil {
		return err
	}
	return ta.Start(ctx)
}

func (ta *TimerAgent) Restart(ctx context.Context) error {
	return ta.Reload(ctx)
}

func (ta *TimerAgent) Reset() {
	// Reset state (called when controller resets)
	ta.mu.Lock()
	defer ta.mu.Unlock()

	if ta.cancel != nil {
		ta.cancel()
		ta.cancel = nil
	}
}

func (ta *TimerAgent) run(ctx context.Context, schedule Schedule) {
	for {
		now := time.Now()
		next := schedule.Next(now)
		duration := next.Sub(now)

		select {
		case <-ctx.Done():
			return
		case <-time.After(duration):
			ta.execute()
		}
	}
}

func (ta *TimerAgent) execute() {
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "timer triggered, executing agent", logattr.AgentID(ta.id))

	// Execute the Python agent (one-shot)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pythonBin := "python"
	if _, err := exec.LookPath(pythonBin); err != nil {
		if _, err3 := exec.LookPath("python3"); err3 == nil {
			pythonBin = "python3"
		}
	}

	cmd := exec.CommandContext(ctx, pythonBin, ta.pyRunner, "--module", ta.path, "--start")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "timer execution failed", logattr.Err(err), logattr.AgentID(ta.id))
		return
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "timer execution completed", logattr.AgentID(ta.id))
}
