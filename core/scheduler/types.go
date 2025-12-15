package scheduler

// Job represents an intent to run an agent.
type Job struct {
	ID           string            `json:"id"`
	AgentID      string            `json:"agent_id"`
	AgentType    string            `json:"agent_type"`
	AssignedNode string            `json:"assigned_node,omitempty"`
	Requirements map[string]string `json:"requirements,omitempty"`
}

// Assignment represents a job assignment to a node.
type Assignment struct {
	JobID  string `json:"job_id"`
	NodeID string `json:"node_id"`
}

// Strategy defines how jobs are placed.
type Strategy string

const (
	StrategyRandom     Strategy = "random"
	StrategyRoundRobin Strategy = "round-robin"
)
