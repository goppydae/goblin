package supervisor

import (
	"encoding/json"
	"fmt"

	"github.com/goppydae/goblin/core/migration"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// Note: MigrateRequest, JobInfo, MemberInfo are defined in scheduler_rpc.go

// RegisterSchedulerHandlers registers all scheduler RPC methods with the QUIC server
func RegisterSchedulerHandlers(server *QUICRPCServer, rpc *SchedulerRPC) {
	// SubmitJob handler
	server.RegisterHandler("SchedulerRPC.SubmitJob", func(payload []byte) ([]byte, error) {
		var req goblinv1.SubmitJobRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.SubmitJobResponse
		if err := rpc.SubmitJob(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// DrainNode handler
	server.RegisterHandler("SchedulerRPC.DrainNode", func(payload []byte) ([]byte, error) {
		var req goblinv1.DrainNodeRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.DrainNodeResponse
		if err := rpc.DrainNode(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// MigrateJob handler
	// Live instance migration (GOBLIN-DIV-031). Distinct from
	// MigrateJob below, which reassigns work rather than moving a
	// running process.
	server.RegisterHandler("SchedulerRPC.MigrateInstance", func(payload []byte) ([]byte, error) {
		var req MigrateInstanceRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.MigrateInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

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
		var req goblinv1.ListJobsRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.ListJobsResponse
		if err := rpc.ListJobs(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// Members handler
	server.RegisterHandler("SchedulerRPC.Members", func(payload []byte) ([]byte, error) {
		var req goblinv1.MembersRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.MembersResponse
		if err := rpc.Members(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// GetEvents handler
	server.RegisterHandler("SchedulerRPC.GetEvents", func(payload []byte) ([]byte, error) {
		var req goblinv1.GetEventsRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.GetEventsResponse
		if err := rpc.GetEvents(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("SchedulerRPC.SignalAgentInstance", func(payload []byte) ([]byte, error) {
		var req SignalAgentInstanceRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.SignalAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// ListAgentInstances handler
	server.RegisterHandler("SchedulerRPC.ListAgentInstances", func(payload []byte) ([]byte, error) {
		var req goblinv1.ListAgentInstancesRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.ListAgentInstancesResponse
		if err := rpc.ListAgentInstances(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("SchedulerRPC.RegisterGlobalAgent", func(payload []byte) ([]byte, error) {
		var req goblinv1.RegisterGlobalAgentRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.RegisterGlobalAgentResponse
		if err := rpc.RegisterGlobalAgent(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// ListGlobalAgents handler
	server.RegisterHandler("SchedulerRPC.ListGlobalAgents", func(payload []byte) ([]byte, error) {
		var req goblinv1.ListGlobalAgentsRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.ListGlobalAgentsResponse
		if err := rpc.ListGlobalAgents(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// GetGlobalAgent handler
	server.RegisterHandler("SchedulerRPC.GetGlobalAgent", func(payload []byte) ([]byte, error) {
		var req goblinv1.GetGlobalAgentRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.GetGlobalAgentResponse
		if err := rpc.GetGlobalAgent(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("SchedulerRPC.ScaleAgent", func(payload []byte) ([]byte, error) {
		var req goblinv1.ScaleAgentRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.ScaleAgentResponse
		if err := rpc.ScaleAgent(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// DeleteGlobalAgent handler
	server.RegisterHandler("SchedulerRPC.DeleteGlobalAgent", func(payload []byte) ([]byte, error) {
		var req goblinv1.DeleteGlobalAgentRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.DeleteGlobalAgentResponse
		if err := rpc.DeleteGlobalAgent(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// PublishEvent handler
	server.RegisterHandler("SchedulerRPC.PublishEvent", func(payload []byte) ([]byte, error) {
		var req goblinv1.PublishEventRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.PublishEventResponse
		if err := rpc.PublishEvent(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// ListLocalAgents handler
	server.RegisterHandler("SchedulerRPC.ListLocalAgents", func(payload []byte) ([]byte, error) {
		var req goblinv1.ListLocalAgentsRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.ListLocalAgentsResponse
		if err := rpc.ListLocalAgents(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
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

	server.RegisterHandler("NodeRPC.SignalAgentInstance", func(payload []byte) ([]byte, error) {
		var req SignalAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.SignalAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	// Migration RPCs (GOBLIN-DIV-031). Names come from constants in
	// core/migration so the caller and this registration cannot drift.
	server.RegisterHandler(migration.MethodCheckpoint, func(payload []byte) ([]byte, error) {
		var req CheckpointAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.CheckpointAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	server.RegisterHandler(migration.MethodRestore, func(payload []byte) ([]byte, error) {
		var req RestoreAgentRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.RestoreAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	})

	server.RegisterHandler(migration.MethodPull, func(payload []byte) ([]byte, error) {
		var req PullCheckpointRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}
		var resp string
		if err := rpc.PullCheckpoint(&req, &resp); err != nil {
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
