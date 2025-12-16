package tui

import protopkg "github.com/goppydae/gapi/pkg/proto"

func actionToEnum(action string) protopkg.LifecycleControl_Action {
	switch action {
	case "start":
		return protopkg.LifecycleControl_START
	case "stop":
		return protopkg.LifecycleControl_STOP
	case "restart":
		return protopkg.LifecycleControl_RESTART
	case "reload":
		return protopkg.LifecycleControl_RELOAD
	default:
		return protopkg.LifecycleControl_ACTION_UNSPECIFIED
	}
}

func stateToString(s protopkg.AgentState) string {
	switch s {
	case protopkg.AgentState_AGENT_STATE_INITIALIZING:
		return "initializing"
	case protopkg.AgentState_AGENT_STATE_INITIALIZED:
		return "initialized"
	case protopkg.AgentState_AGENT_STATE_STARTING:
		return "starting"
	case protopkg.AgentState_AGENT_STATE_STARTED:
		return "started"
	case protopkg.AgentState_AGENT_STATE_RUNNING:
		return "running"
	case protopkg.AgentState_AGENT_STATE_STOPPING:
		return "stopping"
	case protopkg.AgentState_AGENT_STATE_STOPPED:
		return "stopped"
	case protopkg.AgentState_AGENT_STATE_FAILED:
		return "failed"
	case protopkg.AgentState_AGENT_STATE_RELOADING:
		return "reloading"
	default:
		return "unknown"
	}
}
