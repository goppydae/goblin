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
)
