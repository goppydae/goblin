package supervisor

import (
	"encoding/json"
	"fmt"

	"github.com/goppydae/goblin/core/scheduler"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// Note: MigrateRequest, JobInfo, MemberInfo are defined in scheduler_rpc.go

// RegisterSchedulerHandlers registers all scheduler RPC methods with the QUIC server
func RegisterSchedulerHandlers(server *QUICRPCServer, rpc *SchedulerRPC) {
	// SubmitJob handler - takes *scheduler.Job
	server.RegisterHandler("SchedulerRPC.SubmitJob", func(payload []byte) ([]byte, error) {
		var job scheduler.Job
		if err := json.Unmarshal(payload, &job); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}

		var resp string
		if err := rpc.SubmitJob(&job, &resp); err != nil {
			return nil, err
		}

		return json.Marshal(resp)
	})

	// DrainNode handler
	server.RegisterHandler("SchedulerRPC.DrainNode", func(payload []byte) ([]byte, error) {
		var nodeID string
		if err := json.Unmarshal(payload, &nodeID); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}

		var resp []string
		if err := rpc.DrainNode(&nodeID, &resp); err != nil {
			return nil, err
		}

		return json.Marshal(resp)
	})

	// MigrateJob handler
	server.RegisterHandler("SchedulerRPC.MigrateJob", func(payload []byte) ([]byte, error) {
		var req MigrateRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}

		var resp string
		if err := rpc.MigrateJob(&req, &resp); err != nil {
			return nil, err
		}

		return json.Marshal(resp)
	})

	// ListJobs handler
	server.RegisterHandler("SchedulerRPC.ListJobs", func(payload []byte) ([]byte, error) {
		var req struct{}
		// payload might be empty for ListJobs

		var resp []JobInfo
		if err := rpc.ListJobs(&req, &resp); err != nil {
			return nil, err
		}

		return json.Marshal(resp)
	})

	// Members handler
	server.RegisterHandler("SchedulerRPC.Members", func(payload []byte) ([]byte, error) {
		var req struct{}
		// payload might be empty for Members

		var resp []MemberInfo
		if err := rpc.Members(&req, &resp); err != nil {
			return nil, err
		}

		return json.Marshal(resp)
	})

	// Register new GetEvents handler
	server.RegisterHandler("SchedulerRPC.GetEvents", func(payload []byte) ([]byte, error) {
		var req GetEventsRequest
		// If payload is empty, use default request (cursor 0)
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &req); err != nil {
				return nil, fmt.Errorf("invalid request: %w", err)
			}
		}
		var resp []LogEvent
		if err := rpc.GetEvents(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// RegisterGlobalAgent handler
	server.RegisterHandler("SchedulerRPC.ListAgentInstances", func(payload []byte) ([]byte, error) {
		var req ListAgentInstancesRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var out []*goblinv1.AgentInstance
		if err := rpc.ListAgentInstances(&req, &out); err != nil {
			return nil, err
		}
		return json.Marshal(out)
	})

	server.RegisterHandler("SchedulerRPC.RegisterGlobalAgent", func(payload []byte) ([]byte, error) {
		var spec goblinv1.AgentSpec
		if err := proto.Unmarshal(payload, &spec); err != nil {
			// Try JSON fallback if Proto fails (CLI sends JSON usually, but let's see)
			// Actually, existing handlers use JSON unmarshal.
			// Let's stick to JSON for consistency with other handlers in this file.
			if jsonErr := json.Unmarshal(payload, &spec); jsonErr != nil {
				return nil, fmt.Errorf("invalid request (json): %w", jsonErr)
			}
		}

		var resp string
		if err := rpc.RegisterGlobalAgent(&spec, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// ListGlobalAgents handler
	server.RegisterHandler("SchedulerRPC.ListGlobalAgents", func(payload []byte) ([]byte, error) {
		var req struct{}
		// payload empty
		var resp []*goblinv1.AgentSpec
		if err := rpc.ListGlobalAgents(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// GetGlobalAgent handler
	server.RegisterHandler("SchedulerRPC.GetGlobalAgent", func(payload []byte) ([]byte, error) {
		var agentID string
		if err := json.Unmarshal(payload, &agentID); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp goblinv1.AgentSpec
		if err := rpc.GetGlobalAgent(&agentID, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(&resp)
	})

	// ScaleAgent handler
	server.RegisterHandler("SchedulerRPC.ScaleAgent", func(payload []byte) ([]byte, error) {
		var req ScaleAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.ScaleAgent(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// DeleteGlobalAgent handler
	server.RegisterHandler("SchedulerRPC.DeleteGlobalAgent", func(payload []byte) ([]byte, error) {
		var agentID string
		if err := json.Unmarshal(payload, &agentID); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.DeleteGlobalAgent(&agentID, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// PublishEvent handler
	server.RegisterHandler("SchedulerRPC.PublishEvent", func(payload []byte) ([]byte, error) {
		var req PublishRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.PublishEvent(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// ListLocalAgents handler
	server.RegisterHandler("SchedulerRPC.ListLocalAgents", func(payload []byte) ([]byte, error) {
		var req struct{}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp []LocalAgentInfo
		if err := rpc.ListLocalAgents(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})
}

// RegisterNodeHandlers registers node RPC methods
func RegisterNodeHandlers(server *QUICRPCServer, rpc *NodeRPC) {
	server.RegisterHandler("NodeRPC.StartAgentInstance", func(payload []byte) ([]byte, error) {
		var req StartAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.StartAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	server.RegisterHandler("NodeRPC.StopAgentInstance", func(payload []byte) ([]byte, error) {
		var req StopAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.StopAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})
}

// Unused function removed - client-side logic will go in CLI
var _ = proto.Marshal         // Keep proto import for consistency
var _ = goblinv1.RPCRequest{} // Keep goblinv1 import for consistency
