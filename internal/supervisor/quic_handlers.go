// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"fmt"

	"github.com/goppydae/goblin/core/migration"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

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

	// MigrateInstance handler.
	// Live instance migration (GOBLIN-DIV-031). Distinct from
	// MigrateJob below, which reassigns work rather than moving a
	// running process.
	server.RegisterHandler("SchedulerRPC.MigrateInstance", func(payload []byte) ([]byte, error) {
		var req goblinv1.MigrateInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.MigrateInstanceResponse
		if err := rpc.MigrateInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// MigrateJob handler.
	server.RegisterHandler("SchedulerRPC.MigrateJob", func(payload []byte) ([]byte, error) {
		var req goblinv1.MigrateJobRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.MigrateJobResponse
		if err := rpc.MigrateJob(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
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
		var req goblinv1.SignalAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.SignalAgentInstanceResponse
		if err := rpc.SignalAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
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

// RegisterNodeHandlers registers node RPC methods.
func RegisterNodeHandlers(server *QUICRPCServer, rpc *NodeRPC) {
	server.RegisterHandler("NodeRPC.MigrationReady", func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeMigrationReadyRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodeMigrationReadyResponse
		if err := rpc.MigrationReady(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("NodeRPC.StartAgentInstance", func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeStartAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodeStartAgentInstanceResponse
		if err := rpc.StartAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("NodeRPC.SignalAgentInstance", func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeSignalAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodeSignalAgentInstanceResponse
		if err := rpc.SignalAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	// Migration RPCs (GOBLIN-DIV-031). Names come from constants in
	// core/migration so the caller and this registration cannot drift.
	server.RegisterHandler(migration.MethodCheckpoint, func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeCheckpointAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodeCheckpointAgentInstanceResponse
		if err := rpc.CheckpointAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler(migration.MethodRestore, func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeRestoreAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		// RestoreAgentInstance carries a nested Spec message (batch B's
		// lesson): guarded here, at the decode boundary, rather than in
		// the method itself. The method's own check
		// (node_rpc_migration.go) stays a plain error for a direct
		// caller; this classification is what a wire-decoded request
		// gets, matching StartAgentInstance's ErrInvalidRequest but
		// applied at the layer that can see both the message and the
		// error, without core/migration's Caller needing to import it.
		if req.GetSpec() == nil {
			return nil, fmt.Errorf("%w: spec is required", ErrInvalidRequest)
		}
		var resp goblinv1.NodeRestoreAgentInstanceResponse
		if err := rpc.RestoreAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler(migration.MethodPull, func(payload []byte) ([]byte, error) {
		var req goblinv1.NodePullCheckpointRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodePullCheckpointResponse
		if err := rpc.PullCheckpoint(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})

	server.RegisterHandler("NodeRPC.StopAgentInstance", func(payload []byte) ([]byte, error) {
		var req goblinv1.NodeStopAgentInstanceRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
		var resp goblinv1.NodeStopAgentInstanceResponse
		if err := rpc.StopAgentInstance(&req, &resp); err != nil {
			return nil, err
		}
		return proto.Marshal(&resp)
	})
}
