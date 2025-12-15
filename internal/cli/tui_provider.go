package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/goppydae/gapi/core/tui"
)

// ClusterController implements tui.AgentControl for Goblin cluster
type ClusterController struct {
	apiAddr string
}

func NewClusterController(addr string) *ClusterController {
	return &ClusterController{apiAddr: addr}
}

func (c *ClusterController) FetchStatus(ctx context.Context) ([]tui.AgentStatus, error) {
	// 1. Scan assignments
	// Protocol Op 5 = SCAN
	// Prefix = /jobs/assignments/
	val, err := quicRequest(5, "default", "/jobs/assignments/", nil)
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}

	var results map[string][]byte
	if err := json.Unmarshal(val, &results); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scan results: %w", err)
	}

	var statuses []tui.AgentStatus
	for key, jobIDBytes := range results {
		// Key format: /jobs/assignments/<node>/<jobID>
		parts := strings.Split(key, "/")
		// ["", "jobs", "assignments", node, jobID...]
		// Only process valid keys
		if len(parts) >= 5 {
			nodeID := parts[3]
			jobID := string(jobIDBytes)
			// Or jobID could be last part?
			// The value is definitely jobID.

			status := tui.AgentStatus{
				ID:    jobID,
				State: "running", // Implicitly running if assigned
				Type:  nodeID,    // Show Node as Type for visibility
			}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func (c *ClusterController) Lifecycle(ctx context.Context, id, action string) (bool, error) {
	// Logic to stop requires knowing the Node ID to form the key.
	// But `id` passed here is just `jobID`.
	// We need to look up where it is running first?
	// The TUI implementation is singular.
	// This is a limitation.
	// However, we can scan again to find the node, or just fail for now.
	// Or maybe we can support `stop` by scanning first.

	if action == "stop" {
		// Find the job
		statuses, err := c.FetchStatus(ctx)
		if err != nil {
			return false, err
		}
		var targetNode string
		for _, s := range statuses {
			if s.ID == id {
				targetNode = s.Type // We stored Node in Type
				break
			}
		}

		if targetNode == "" {
			return false, fmt.Errorf("job %s not found running", id)
		}

		// Delete assignment
		key := path.Join("/jobs/assignments", targetNode, id)
		if _, err := quicRequest(3, "default", key, nil); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, fmt.Errorf("action %s not supported in cluster mode yet", action)
}

func (c *ClusterController) GetLogs(ctx context.Context, id string) (<-chan string, error) {
	// TODO: Implement distributed log streaming via QUIC
	ch := make(chan string)
	close(ch)
	return ch, fmt.Errorf("distributed logs not implemented yet")
}

// Redefine quicRequest if it's not exported?
// It's in the same package (cli), so it should be visible if defined in cli.go
// It is defined in cli.go as `func quicRequest(...)`. It is unexported (lowercase q).
// But same package access allows it.
