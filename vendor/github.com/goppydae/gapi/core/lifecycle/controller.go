// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/goppydae/gapi/internal/ident"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
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

	// startMu/starting are the re-entrancy guard for ActionStart.
	//
	// THIS EXISTS BECAUSE STATE STOPPED BEING THE GUARD (operator
	// decision 42, GAPI-DIV-104). The early transition to StateStarting
	// used to be set before the dependency walk, and the switch at the
	// top of ActionStart read it to make a concurrent start a no-op - so
	// one field was serving as both the reported state and the in-flight
	// marker. Moving the transition to after the exec, where STARTING
	// becomes an observation rather than an intention, takes the marker
	// away with it and leaves a window in which two callers could both
	// walk the tree and both spawn.
	//
	// An explicit marker is the honest form: the two facts have
	// different lifetimes and only one of them is anybody else's
	// business.
	startMu  sync.Mutex
	starting bool
}

// beginStart claims the start-in-flight marker, reporting false if
// another caller already holds it.
func (c *Controller) beginStart() bool {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	if c.starting {
		return false
	}
	c.starting = true
	return true
}

func (c *Controller) endStart() {
	c.startMu.Lock()
	c.starting = false
	c.startMu.Unlock()
}

// depCtxKey is an unexported context-key type so the dependency-cycle
// stack cannot collide with other packages' context values (SA1029).
type depCtxKey struct{}

// DepCtxKey is used to store the call stack for cycle detection
var DepCtxKey = depCtxKey{}

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

	_ = c.bus.Subscribe("system", "", eventbus.TopicAgentLifecycleStatus, func(ev eventbus.Event[*anypb.Any]) {
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
		// Structural only (R16): every in-tree producer populates the
		// run_id field; the legacy message-text parse is gone.
		runID := strings.TrimSpace(st.GetRunId())

		evt := statusEvt{state: got, when: when, runID: runID}
		select {
		case c.stateCh <- evt:
		default:
			// Channel full: evict the oldest event to make room for the newest,
			// so a fresh running/terminal status is never lost behind a backlog
			// of stale events during rapid start/stop churn (which previously
			// caused awaitRunning to time out on a healthy agent).
			select {
			case <-c.stateCh:
			default:
			}
			select {
			case c.stateCh <- evt:
			default:
			}
		}
	})

	return c
}

func (c *Controller) State() string { return c.sm.GetState() }

// dependencyClasses splits this controller's dependencies into hard (Requires)
// and soft (Wants). Hard failures block startup; soft failures only warn. If the
// resolver does not distinguish the two, every dependency is treated as hard so
// existing behavior is preserved.
func (c *Controller) dependencyClasses() (hard, soft []string) {
	type classified interface {
		HardDepsOf(id string) []string
		SoftDepsOf(id string) []string
	}
	if cr, ok := c.deps.(classified); ok {
		return cr.HardDepsOf(c.id), cr.SoftDepsOf(c.id)
	}
	return c.deps.DepsOf(c.id), nil
}

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
		// The state check above is necessary and no longer sufficient:
		// STARTING is now set after the exec, so between here and there
		// a second caller sees a startable state. See beginStart.
		if !c.beginStart() {
			return nil
		}
		defer c.endStart()
		if c.deps != nil {
			hard, soft := c.dependencyClasses()
			for _, dep := range hard {
				if err := c.deps.EnsureStarted(ctx, dep); err != nil {
					return fmt.Errorf("dependency %q failed to start: %w", dep, err)
				}
			}
			// Soft (Wants) dependencies are advisory: a failure is logged and
			// startup continues, rather than blocking like a hard dependency.
			for _, dep := range soft {
				if err := c.deps.EnsureStarted(ctx, dep); err != nil {
					slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "soft (wants) dependency failed to start; continuing", logattr.Module("lifecycle"), logattr.AgentID(c.id), logattr.Dependency(dep), logattr.Err(err))
				}
			}
		}
		c.publishControl(protopkg.LifecycleControl_ACTION_START)

		runID := ident.NewV7String()
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
			// Derive from the caller's context so cancellation, deadlines, and
			// tracing propagate into the runner instead of being discarded.
			//
			// This context bounds the Start *call* and nothing else. cancel is
			// invoked as soon as Start returns rather than deferred to this
			// function's exit, so the window in which it could reach a started
			// process is as small as the language allows - and Runner
			// implementations must not tie a spawned process to it at all
			// (GAPI-DIV-028). Readiness has its own deadline below.
			startCtx, cancel := context.WithTimeout(ctx, c.WaitStart)
			err := c.runner.Start(startCtx)
			cancel()
			if err != nil {
				_ = c.sm.TransitionTo(StateError)
				return err
			}
		}

		// STARTING NOW, AND NOT BEFORE (operator decision 42). Start
		// returned without error, so a child exists. The runner
		// published the observation with the run id at the instant of
		// the exec; this is the state machine agreeing with it.
		if err := c.sm.TransitionTo(StateStarting); err != nil {
			return err
		}

		if err := c.awaitRunningWithRunIDSince(c.WaitStart, runID, cutover); err != nil {
			_ = c.sm.TransitionTo(StateError)
			// A DEADLINE THAT EXPIRES IS REPORTED, NOT ONLY RETURNED.
			// The caller gets the error, but nothing on the bus said the
			// start failed - so an operator watching the status topic saw
			// STARTING and then nothing at all (GAPI-DIV-104).
			c.publishFailed(runID, err.Error())
			return fmt.Errorf("start: %w", err)
		}
		return c.sm.TransitionTo(StateRunning)

	case ActionStop:
		if c.sm.GetState() == StateStopped {
			return nil
		}

		if c.sm.GetState() != StateStopping {
			c.publishControl(protopkg.LifecycleControl_ACTION_STOP)
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
		c.publishControl(protopkg.LifecycleControl_ACTION_RELOAD)
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
		c.publishControl(protopkg.LifecycleControl_ACTION_RESTART)
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
	// Advisory observability event: a publish failure is logged loudly but
	// must not abort the lifecycle action itself (aborting stop/start on a
	// closed bus would invert priorities during shutdown).
	if err := c.bus.Publish(eventbus.NewEvent("system", "", eventbus.TopicAgentLifecycleControl, c.id, anyMsg, true)); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to publish lifecycle control event", logattr.Module("lifecycle"), logattr.AgentID(c.id), logattr.Err(err))
	}
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
	// Advisory observability event; see publishControl for the no-abort rationale.
	if err := c.bus.Publish(eventbus.NewEvent("system", "", eventbus.TopicAgentLifecycleStatus, c.id, anyMsg, true)); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to publish lifecycle status event", logattr.Module("lifecycle"), logattr.AgentID(c.id), logattr.Err(err))
	}
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
			return c.startTimeout(d, wantRunID)
		}
	}
}

// StartTimeout is the start deadline expiring, as data rather than as a
// sentence (GAPI-DIV-104).
//
// Silent is the discriminator the old bare timeout could not express: a
// child that has written nothing is hung before its first report or was
// built against an ADK that never opened the control descriptor, while
// one that has spoken and not reached RUNNING is merely slow. Those want
// different operator responses, and a caller must be able to branch on
// the difference without matching on a message.
//
// SilenceKnown is separate from Silent because "the runner cannot answer
// this question" is a third state, not a quiet false. An in-process
// runner has no control channel at all.
type StartTimeout struct {
	AgentID      string
	RunID        string
	Waited       time.Duration
	Silent       bool
	SilenceKnown bool
}

func (e *StartTimeout) Error() string {
	switch {
	case e.SilenceKnown && e.Silent:
		return fmt.Sprintf(
			"agent %s was spawned and wrote no control frame within %s (run_id=%s): hung before its first report, or its ADK never opened the control descriptor",
			e.AgentID, e.Waited, e.RunID)
	case e.SilenceKnown:
		return fmt.Sprintf(
			"agent %s spoke but did not reach running within %s (run_id=%s)",
			e.AgentID, e.Waited, e.RunID)
	default:
		return fmt.Sprintf(
			"timeout waiting for agent %s state=running after %s (run_id=%s)",
			e.AgentID, e.Waited, e.RunID)
	}
}

func (c *Controller) startTimeout(d time.Duration, runID string) error {
	e := &StartTimeout{AgentID: c.id, RunID: runID, Waited: d}
	if sr, ok := c.runner.(SpeechReporter); ok {
		e.SilenceKnown = true
		e.Silent = !sr.HasSpoken()
	}
	return e
}

// publishFailed announces a supervisor-observed failure on the status
// topic, carrying the run id so it can be told from the restart behind
// it. Advisory observability event; see publishControl.
func (c *Controller) publishFailed(runID, message string) {
	st := &protopkg.LifecycleStatus{
		AgentId:  c.id,
		State:    "FAILED",
		Message:  message,
		Time:     timestamppb.Now(),
		Hostname: c.host,
		RunId:    runID,
	}
	anyMsg, _ := anypb.New(st)
	if err := c.bus.Publish(eventbus.NewEvent("system", "", eventbus.TopicAgentLifecycleStatus, c.id, anyMsg, true)); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to publish start-failure status", logattr.Module("lifecycle"), logattr.AgentID(c.id), logattr.Err(err))
	}
}

func anyEnumToString(e protopkg.AgentState) string {
	s := strings.TrimPrefix(e.String(), "AGENT_STATE_")
	return strings.ToLower(s)
}
