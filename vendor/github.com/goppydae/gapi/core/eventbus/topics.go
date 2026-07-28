package eventbus

// Well-known cross-boundary topics. One exported constant per topic; the
// orchestrator imports these rather than repeating string literals
// (GAPI-DIV-010; unblocks GOBLIN-DIV-011). Purely in-package topics may stay
// literals - only cross-boundary topics are promoted here.
const (
	// TopicAgentNetworkRunning announces that the network agent reached its
	// running state; the supervisors' boot phase gate waits on it. Payload:
	// reserved (no producer publishes a typed payload yet; the gate only
	// observes arrival).
	TopicAgentNetworkRunning = "agent.network.running"

	// TopicPrefixAgentLifecycle is the prefix under which every lifecycle
	// topic below lives; prefix subscribers use it with SubscribePrefix
	// (mind the type hazard documented there - sibling topics carry
	// different payload types).
	TopicPrefixAgentLifecycle = "agent/lifecycle"

	// TopicAgentLifecycleAction is the operator/client -> supervisor
	// command channel. Payload: LifecycleControl. Distinct from
	// TopicAgentLifecycleControl despite the confusable name: this one
	// carries requests to act.
	TopicAgentLifecycleAction = "agent/lifecycle.action"

	// TopicAgentLifecycleControl carries the lifecycle controller's own
	// announcements of actions it is applying. Payload: LifecycleControl.
	TopicAgentLifecycleControl = "agent/lifecycle.control"

	// TopicAgentLifecycleStatus carries agent state reports. Payload:
	// LifecycleStatus (run_id is the structural restart-detection key).
	TopicAgentLifecycleStatus = "agent/lifecycle.status"

	// TopicAgentLifecycleTransition carries state-machine transition
	// events. Payload: LifecycleTransition.
	TopicAgentLifecycleTransition = "agent/lifecycle.transition"
)
