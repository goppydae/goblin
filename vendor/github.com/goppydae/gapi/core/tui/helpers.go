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
