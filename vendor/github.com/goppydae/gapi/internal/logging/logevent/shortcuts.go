package logevent

import "github.com/rs/zerolog"

func Lifecycle(l zerolog.Logger, source, action, agentID, version string) {
	Log(l, Event{
		Type:   "lifecycle",
		Source: source,
		Payload: LifecyclePayload{
			Action:  action,
			AgentID: agentID,
			Version: version,
		},
	})
}

func EventBus(l zerolog.Logger, source, topic, message string) {
	Log(l, Event{
		Type:   "eventbus",
		Source: source,
		Payload: BusPayload{
			Topic:   topic,
			Payload: message,
		},
	})
}
