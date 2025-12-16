package lifecycle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/internal/eventbus"
	protopkg "github.com/goppydae/gapi/internal/proto"
)

type Action string

const (
	ActionInitialize Action = "initialize"
	ActionStart      Action = "start"
	ActionStop       Action = "stop"
	ActionRestart    Action = "restart"
	ActionReload     Action = "reload"
)

type TypedBus = eventbus.EventBus[*anypb.Any]

type statusEvt struct {
	state string
	when  time.Time
	runID string
}

var reRunID = regexp.MustCompile(`(?:^|\s)run_id=([a-f0-9\-]{8,})\b`)

type Controller struct {
	id        string
	host      string
	runner    Runner
	sm        *LifecycleStateMachine
	bus       *TypedBus
	deps      DependencyResolver
	GraceStop time.Duration
	WaitStart time.Duration
	WaitStop  time.Duration
	stateCh   chan statusEvt // single, long-lived feed
}

// DepCtxKey is used to store the call stack for cycle detection
var DepCtxKey = "gapi_dep_stack"

type DependencyResolver interface {
	DepsOf(id string) []string
	IsRunning(id string) bool
	EnsureStarted(ctx context.Context, id string) error
}

func NewController(id, host string, r Runner, bus *TypedBus, deps DependencyResolver) *Controller {
	c := &Controller{
		id:        id,
		host:      host,
		runner:    r,
		sm:        NewLifecycleStateMachine(id, host, bus),
		bus:       bus,
		deps:      deps,
		GraceStop: 3 * time.Second,
		WaitStart: 10 * time.Second,
		WaitStop:  5 * time.Second,
		stateCh:   make(chan statusEvt, 64),
	}

	_ = c.bus.Subscribe("system", "", "agent/lifecycle.status", func(ev eventbus.Event[*anypb.Any]) {
		if ev.Payload == nil {
			return
		}
		var st protopkg.LifecycleStatus
		if err := ev.Payload.UnmarshalTo(&st); err != nil {
			return
		}
		if st.AgentId != c.id {
			return
		}
		got := strings.ToLower(strings.TrimSpace(st.State))
		if got == "" {
			got = strings.ToLower(strings.TrimSpace(st.GetState()))
		}
		if got == "" {
			return
		}
		when := time.Now()
		if st.Time != nil {
			when = st.Time.AsTime()
		}
		msg := strings.TrimSpace(st.Message)
		runID := ""
		if m := reRunID.FindStringSubmatch(msg); len(m) == 2 {
			runID = m[1]
		}
		select {
		case c.stateCh <- statusEvt{state: got, when: when, runID: runID}:
		default:
		}
	})

	return c
}

func (c *Controller) State() string { return c.sm.GetState() }

func (c *Controller) Apply(a Action) error {
	return c.ApplyWithContext(context.Background(), a)
}

func (c *Controller) ApplyWithContext(ctx context.Context, a Action) error {
	// Cycle Detection
	stack, _ := ctx.Value(DepCtxKey).([]string)
	for _, visited := range stack {
		if visited == c.id {
			return fmt.Errorf("dependency cycle detected: %s -> ... -> %s", strings.Join(stack, " -> "), c.id)
		}
	}
	stack = append(stack, c.id)
	ctx = context.WithValue(ctx, DepCtxKey, stack)

	switch a {
	case ActionInitialize:
		return c.sm.TransitionTo(StateInitializing)

	case ActionStart:
		switch c.sm.GetState() {
		case StateStarting, StateRunning, StateReloading:
			return nil
		}
		if c.deps != nil {
			for _, dep := range c.deps.DepsOf(c.id) {
				if err := c.deps.EnsureStarted(ctx, dep); err != nil {
					return fmt.Errorf("dependency %q failed to start: %w", dep, err)
				}
			}
		}
		c.publishControl(protopkg.LifecycleControl_START)
		if err := c.sm.TransitionTo(StateStarting); err != nil {
			return err
		}

		runID := uuid.New().String()
		if s, ok := c.runner.(RunIDSetter); ok {
			s.SetRunID(runID)
		}

		// drain stale events
	Drain:
		for {
			select {
			case <-c.stateCh:
			default:
				break Drain
			}
		}
		cutover := time.Now()

		// spawn
		{
			ctx, cancel := context.WithTimeout(context.Background(), c.WaitStart)
			defer cancel()
			if err := c.runner.Start(ctx); err != nil {
				_ = c.sm.TransitionTo(StateError)
				return err
			}
		}

		if err := c.awaitRunningWithRunIDSince(c.WaitStart, runID, cutover); err != nil {
			_ = c.sm.TransitionTo(StateError)
			return fmt.Errorf("start: %w", err)
		}
		return c.sm.TransitionTo(StateRunning)

	case ActionStop:
		if c.sm.GetState() == StateStopped {
			return nil
		}

		if c.sm.GetState() != StateStopping {
			c.publishControl(protopkg.LifecycleControl_STOP)
			if err := c.sm.TransitionTo(StateStopping); err != nil {
				return err
			}
		}

		// Graceful first, then kill by runner implementation.
		stopCtx, stopCancel := context.WithTimeout(context.Background(), c.GraceStop)
		_ = c.runner.Stop(stopCtx) // ignore exact error; we take ownership
		stopCancel()

		// Always cleanup OS/process & transport handles.
		if x, ok := c.runner.(interface{ Reset() }); ok {
			x.Reset()
		}

		// Supervisor-owned STOPPED so clients don't hang.
		c.publishStatus(protopkg.AgentState_AGENT_STATE_STOPPED, "process exited (supervisor-confirmed)")
		_ = c.sm.TransitionTo(StateStopped)
		return nil

	case ActionReload:
		if c.sm.GetState() != StateRunning {
			return c.ApplyWithContext(ctx, ActionStart)
		}
		c.publishControl(protopkg.LifecycleControl_RELOAD)
		if err := c.sm.TransitionTo(StateReloading); err != nil {
			return err
		}
		// reloadCtx is for the actual reload operation, not cycle detection
		reloadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := c.runner.Reload(reloadCtx); err != nil {
			_ = c.sm.TransitionTo(StateError)
			return err
		}
		if err := c.awaitTarget(c.WaitStart, anyEnumToString(protopkg.AgentState_AGENT_STATE_RUNNING)); err != nil {
			_ = c.sm.TransitionTo(StateError)
			return fmt.Errorf("reload: %w", err)
		}
		return c.sm.TransitionTo(StateRunning)

	case ActionRestart:
		c.publishControl(protopkg.LifecycleControl_RESTART)
		if err := c.ApplyWithContext(ctx, ActionStop); err != nil {
			return fmt.Errorf("restart/stop: %w", err)
		}
		return c.ApplyWithContext(ctx, ActionStart)
	}
	return fmt.Errorf("unknown action %q", a)
}

func (c *Controller) publishControl(a protopkg.LifecycleControl_Action) {
	msg := &protopkg.LifecycleControl{AgentId: c.id, Action: a}
	anyMsg, _ := anypb.New(msg)
	c.bus.Publish(eventbus.NewEvent("system", "", "agent/lifecycle.control", c.id, anyMsg, true))
}

func (c *Controller) publishStatus(state protopkg.AgentState, message string) {
	st := &protopkg.LifecycleStatus{
		AgentId:  c.id,
		State:    state.String(),
		Message:  message,
		Time:     timestamppb.Now(),
		Hostname: c.host,
	}
	anyMsg, _ := anypb.New(st)
	c.bus.Publish(eventbus.NewEvent("system", "", "agent/lifecycle.status", c.id, anyMsg, true))
}

func (c *Controller) awaitTarget(d time.Duration, want string) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	want = strings.ToLower(strings.TrimSpace(want))
	for {
		select {
		case got := <-c.stateCh:
			if strings.EqualFold(got.state, want) {
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("timeout waiting for agent state=%s", want)
		}
	}
}

func (c *Controller) awaitRunningWithRunIDSince(d time.Duration, wantRunID string, since time.Time) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case ev := <-c.stateCh:
			if ev.when.Before(since) {
				continue
			}
			if ev.state == "running" && ev.runID == wantRunID {
				return nil
			}
		case <-timer.C:
			return fmt.Errorf("timeout waiting for agent state=running (run_id=%s)", wantRunID)
		}
	}
}

func anyEnumToString(e protopkg.AgentState) string {
	s := e.String()
	const p = "AGENT_STATE_"
	if strings.HasPrefix(s, p) {
		s = s[len(p):]
	}
	return strings.ToLower(s)
}
