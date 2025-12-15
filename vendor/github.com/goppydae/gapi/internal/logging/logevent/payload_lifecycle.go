package logevent

type LifecyclePayload struct {
	Action  string `json:"action"` // e.g., "start", "stop", "reload"
	AgentID string `json:"agent_id"`
	Version string `json:"version,omitempty"`
}
