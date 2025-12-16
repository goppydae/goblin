package agentmgr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/internal/eventbus"
	"github.com/goppydae/gapi/internal/lifecycle"
	"github.com/rs/zerolog/log"
)

// TimerAgent executes a Python agent on a schedule
type TimerAgent struct {
	id       string
	path     string
	schedule string // systemd-style: OnBootSec=5s, OnUnitActiveSec=30s, etc.
	pyRunner string
	hostname string

	ctrl   *lifecycle.Controller
	cancel context.CancelFunc
	mu     sync.Mutex
}

func NewTimerAgent(id, path, schedule, pyRunner string, bus *eventbus.EventBus[*anypb.Any], lbus *lifecycle.TypedBus) *TimerAgent {
	host, _ := os.Hostname()
	ta := &TimerAgent{
		id:       id,
		path:     path,
		schedule: schedule,
		pyRunner: pyRunner,
		hostname: host,
	}

	// Create controller with proper signature
	ta.ctrl = lifecycle.NewController(id, host, ta, lbus, nil)

	return ta
}

func (ta *TimerAgent) ID() string                        { return ta.id }
func (ta *TimerAgent) Type() string                      { return "timer" }
func (ta *TimerAgent) Lang() string                      { return "python" }
func (ta *TimerAgent) Dependencies() []string            { return nil }
func (ta *TimerAgent) Requires() []string                { return nil }
func (ta *TimerAgent) Wants() []string                   { return nil }
func (ta *TimerAgent) SetRunID(string)                   { /* no-op for timers or store if needed */ }
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

func (ta *TimerAgent) Start(ctx context.Context) error {
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

	log.Info().
		Str("agent", ta.id).
		Str("schedule", ta.schedule).
		Msg("timer agent started")

	// Start ticker goroutine
	ctx, cancel := context.WithCancel(context.Background())
	ta.cancel = cancel
	go ta.run(ctx, schedule)

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

	log.Info().Str("agent", ta.id).Msg("timer agent stopped")
	return nil
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
	log.Info().Str("agent", ta.id).Msg("timer triggered, executing agent")

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
		log.Error().Err(err).Str("agent", ta.id).Msg("timer execution failed")
		return
	}

	log.Info().Str("agent", ta.id).Msg("timer execution completed")
}
