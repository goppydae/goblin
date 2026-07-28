package lifecycle

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/state"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

const (
	StateInitializing = "initializing"
	StateInitialized  = "initialized"
	StateStarting     = "starting"
	StateRunning      = "running"
	StateStopping     = "stopping"
	StateStopped      = "stopped"
	StateReloading    = "reloading"
	StateRestarting   = "restarting"
	StateError        = "error"
)

type LifecycleStateMachine struct {
	sm      *state.BaseStateMachine
	agentID string
	host    string
	bus     *eventbus.EventBus[*anypb.Any]
}

func NewLifecycleStateMachine(agentID, hostname string, bus *eventbus.EventBus[*anypb.Any]) *LifecycleStateMachine {
	sm := state.NewBaseStateMachine(StateStopped, map[string][]string{
		StateInitializing: {StateInitialized, StateStarting, StateError},
		StateInitialized:  {StateStarting, StateRestarting, StateStopped, StateError},
		StateStarting:     {StateRunning, StateError},
		StateRunning:      {StateStopping, StateReloading, StateRestarting, StateError},
		StateStopping:     {StateStopped, StateError},
		StateStopped:      {StateStarting, StateRestarting, StateError},
		StateReloading:    {StateRunning, StateError},
		StateError:        {StateStopping, StateStopped, StateStarting, StateRestarting},
	})

	lsm := &LifecycleStateMachine{
		sm:      sm,
		agentID: agentID,
		host:    hostname,
		bus:     bus,
	}

	sm.OnTransition(func(from, to string) {
		lsm.emitLifecycleEvent(from, to)
	})

	return lsm
}

func (lsm *LifecycleStateMachine) emitLifecycleEvent(from, to string) {
	msg := &protopkg.LifecycleTransition{
		AgentId:  lsm.agentID,
		From:     from,
		To:       to,
		Hostname: lsm.host,
		Time:     timestamppb.Now(),
	}

	anyMsg, err := anypb.New(msg)
	if err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to marshal lifecycle transition event", logattr.Module("lifecycle"), logattr.AgentID(lsm.agentID), logattr.Err(err))
		return
	}

	event := eventbus.NewEvent(
		"system",
		"", // empty namespace
		"agent/lifecycle.transition",
		lsm.agentID,
		anyMsg,
		true,
	)

	// Advisory observability event: log failures loudly, never abort the
	// transition itself (see Controller.publishControl).
	if err := lsm.bus.Publish(event); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to publish lifecycle transition event", logattr.Module("lifecycle"), logattr.AgentID(lsm.agentID), logattr.Err(err))
	}
}

func (lsm *LifecycleStateMachine) TransitionTo(newState string) error {
	return lsm.sm.TransitionTo(newState)
}

func (lsm *LifecycleStateMachine) GetState() string {
	return lsm.sm.GetState()
}

func (lsm *LifecycleStateMachine) CurrentProtoState() protopkg.AgentState {
	switch lsm.GetState() {
	case "initializing":
		return protopkg.AgentState_AGENT_STATE_INITIALIZING
	case "initialized":
		return protopkg.AgentState_AGENT_STATE_INITIALIZED
	case "starting":
		return protopkg.AgentState_AGENT_STATE_STARTING
	case "started":
		return protopkg.AgentState_AGENT_STATE_STARTED
	case "running":
		return protopkg.AgentState_AGENT_STATE_RUNNING
	case "stopping":
		return protopkg.AgentState_AGENT_STATE_STOPPING
	case "stopped":
		return protopkg.AgentState_AGENT_STATE_STOPPED
	case "error":
		return protopkg.AgentState_AGENT_STATE_FAILED
	case "reloading":
		return protopkg.AgentState_AGENT_STATE_RELOADING
	default:
		return protopkg.AgentState_AGENT_STATE_UNSPECIFIED
	}
}
