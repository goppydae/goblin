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
	// enabled is the resolved ENABLED metadata. Default true:
	// a runner constructed without discovery (tests, direct use)
	// must not be silently un-startable.
	enabled bool

	id       string
	path     string
	schedule string // systemd-style: OnBootSec=5s, OnUnitActiveSec=30s, etc.
	// lang selects how a fire is executed: "python" runs the module
	// through pyRunner, "go" executes the binary at path directly.
	// Scheduling itself is language-agnostic; only the fire is not.
	lang     string
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

// NewTimerAgent schedules a Python agent module, executed through the
// ADK runner.
func NewTimerAgent(id, path, schedule, pyRunner string, bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus) *TimerAgent {
	return newTimerAgent(id, path, schedule, "python", pyRunner, lbus)
}

// NewBinaryTimerAgent schedules an executable agent, run directly.
//
// Timers used to be Python-only: discovery routed TYPE=timer here just
// for .py paths, and everything else became a GoAgent, which has no
// scheduling code at all - so a Go timer ran once at discovery and never
// again while its declared SCHEDULE was silently discarded
// (GAPI-DIV-037). The two ADKs are meant to have identical semantics.
func NewBinaryTimerAgent(id, path, schedule string, bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus) *TimerAgent {
	return newTimerAgent(id, path, schedule, "go", "", lbus)
}

func newTimerAgent(id, path, schedule, lang, pyRunner string, lbus *lifecycle.TypedBus) *TimerAgent {
	host, _ := os.Hostname()
	ta := &TimerAgent{
		enabled:  true,
		id:       id,
		path:     path,
		schedule: schedule,
		lang:     lang,
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
func (ta *TimerAgent) Lang() string           { return ta.lang }
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
		"language": ta.lang,
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

		// A zero next is a schedule that will not fire again - the
		// one-shot forms after their single fire. Stop the loop rather
		// than busy-waiting on a zero duration.
		if next.IsZero() {
			slog.Default().LogAttrs(ctx, slog.LevelInfo,
				"timer schedule exhausted; no further fires",
				logattr.AgentID(ta.id), slog.String("schedule", ta.schedule))
			return
		}

		// An elapse point in the past fires immediately, once. Without
		// the clamp a negative duration makes time.After fire at once
		// anyway, but the intent should be explicit.
		duration := max(next.Sub(now), 0)

		select {
		case <-ctx.Done():
			return
		case <-time.After(duration):
			ta.execute(ctx)
		}
	}
}

// TimerFireTimeout bounds a single timer fire. Work that runs longer is
// killed mid-flight, so a scheduled job expecting more time must either
// bound itself below this or be restructured as a service.
const TimerFireTimeout = 30 * time.Second

// fireCommand builds the command for one fire. Python modules go through
// the ADK runner; an executable agent is run directly, which is what its
// own main does when not asked to --describe.
func (ta *TimerAgent) fireCommand(ctx context.Context) *exec.Cmd {
	if ta.lang != "python" {
		return exec.CommandContext(ctx, ta.path)
	}

	pythonBin := "python"
	if _, err := exec.LookPath(pythonBin); err != nil {
		if _, err3 := exec.LookPath("python3"); err3 == nil {
			pythonBin = "python3"
		}
	}

	// --id and --type are not optional here. Without --type the runner
	// defaulted to "service", entered its supervision loop and never
	// exited, so cmd.Run blocked until the deadline and the next fire
	// never happened (GAPI-DIV-039).
	return exec.CommandContext(ctx, pythonBin, ta.pyRunner,
		"--module", ta.path,
		"--id", ta.id,
		"--type", "timer",
		"--start")
}

// execute runs one fire. runCtx is the timer's own loop context, so
// Stop cancels a fire that is already in flight instead of leaving it to
// run out the full timeout after the agent has been told to stop.
func (ta *TimerAgent) execute(runCtx context.Context) {
	slog.Default().LogAttrs(runCtx, slog.LevelInfo, "timer triggered, executing agent", logattr.AgentID(ta.id))

	// Each fire is a one-shot: the process must run to completion and
	// exit, because the next fire is not scheduled until this one
	// returns.
	ctx, cancel := context.WithTimeout(runCtx, TimerFireTimeout)
	defer cancel()

	cmd := ta.fireCommand(ctx)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		slog.Default().LogAttrs(runCtx, slog.LevelError, "timer execution failed", logattr.Err(err), logattr.AgentID(ta.id))
		return
	}

	slog.Default().LogAttrs(runCtx, slog.LevelInfo, "timer execution completed", logattr.AgentID(ta.id))
}

// SetEnabled records whether this agent should be started
// automatically. Set from discovery metadata; absent metadata
// means enabled.
func (a *TimerAgent) SetEnabled(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.enabled = v
}

// Enabled reports whether this agent is started automatically.
// A disabled agent is still discovered and registered, and can
// still be started explicitly - the systemd model.
func (a *TimerAgent) Enabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enabled
}
