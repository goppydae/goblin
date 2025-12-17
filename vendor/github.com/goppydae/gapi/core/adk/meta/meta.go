package meta

// AgentInfo represents all structured metadata discovered from a Python agent file.
type AgentInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Type        string   `json:"type"` // daemon, timer, etc.
	Description string   `json:"description"`
	Interval    int      `json:"interval"`
	Enabled     bool     `json:"enabled"`
	Implements  []string `json:"implements"` // initialize, start, stop, etc.
}
