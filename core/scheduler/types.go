package scheduler

// Job represents an intent to run an agent.
type Job struct {
	ID            string            `json:"id"`
	AgentID       string            `json:"agent_id"`
	AgentType     string            `json:"agent_type"`
	AssignedNode  string            `json:"assigned_node,omitempty"`
	Resources     ResourceReq       `json:"resources,omitempty"`
	Constraints   map[string]string `json:"constraints,omitempty"`
	Requirements  map[string]string `json:"requirements,omitempty"`
	RestartPolicy string            `json:"restart_policy,omitempty"` // always, on-failure, never
	Env           map[string]string `json:"env,omitempty"`
}

// ResourceReq defines CPU and Memory requirements
type ResourceReq struct {
	CPU    float64 `json:"cpu"`    // Cores
	Memory int64   `json:"memory"` // Bytes
}

// Assignment represents a job assignment to a node.
type Assignment struct {
	JobID  string `json:"job_id"`
	NodeID string `json:"node_id"`
}

// Strategy defines how jobs are placed.
type Strategy string

const (
	StrategyRandom      Strategy = "random"
	StrategyRoundRobin  Strategy = "round-robin"
	StrategyBinPack     Strategy = "bin-pack"
	StrategyLeastLoaded Strategy = "least-loaded"
)
