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
}

// Unused function removed - client-side logic will go in CLI
var _ = proto.Marshal         // Keep proto import for consistency
var _ = goblinv1.RPCRequest{} // Keep goblinv1 import for consistency
