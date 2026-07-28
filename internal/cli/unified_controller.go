package cli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/gapi/core/tui"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// UnifiedController provides a unified view of cluster and local state
type UnifiedController struct {
	rpcAddr  string
	gapiAddr string
}

// NewUnifiedController creates a new unified controller
func NewUnifiedController(rpcAddr, gapiAddr string) *UnifiedController {
	return &UnifiedController{
		rpcAddr:  rpcAddr,
		gapiAddr: gapiAddr,
	}
}

// FetchStatus returns combined status from cluster members, jobs, and local agents
func (u *UnifiedController) FetchStatus(ctx context.Context) (_ []tui.AgentStatus, err error) {
	var statuses []tui.AgentStatus

	// Connect to QUIC RPC
	client, err := NewQUICRPCClient(u.rpcAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to QUIC RPC: %w", err)
	}
	defer closeClient(client, &err)

	// Section 1: Cluster Members
	type MemberShort struct {
		Name   string
		Status string
	}

	var members []MemberShort
	if err := client.Call("SchedulerRPC.Members", struct{}{}, &members); err != nil {
		return nil, fmt.Errorf("failed to get cluster members: %w", err)
	}

	for _, m := range members {
		statuses = append(statuses, tui.AgentStatus{
			ID:    m.Name,
			Type:  "cluster-member",
			State: m.Status,
		})
	}

	// Section 2: Jobs
	var jobs []supervisor.JobInfo
	if err := client.Call("SchedulerRPC.ListJobs", struct{}{}, &jobs); err != nil {
		return nil, fmt.Errorf("failed to get jobs: %w", err)
	}

	for _, job := range jobs {
		statuses = append(statuses, tui.AgentStatus{
			ID:    job.JobID,
			Type:  job.AgentType,
			State: job.Status,
		})
	}

	// Section 3: Local GAPI Agents (via embedded core)
	var localAgents []supervisor.LocalAgentInfo
	if err := client.Call("SchedulerRPC.ListLocalAgents", struct{}{}, &localAgents); err != nil {
		// Log warning but continue - local agents may not be enabled
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "could not fetch local agents", logattr.Err(err))
	} else {
		for _, agent := range localAgents {
			statuses = append(statuses, tui.AgentStatus{
				ID:    agent.ID,
				Type:  agent.Type,
				State: agent.State,
			})
		}
	}

	// Section 2.5: Global Agents (Specs)
	var globalSpecs []*goblinv1.AgentSpec
	if err := client.Call("SchedulerRPC.ListGlobalAgents", struct{}{}, &globalSpecs); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to list global agents", logattr.Err(err))
	} else {
		for _, spec := range globalSpecs {
			// Format state to show replicas
			// We don't have real-time health of spec here easily without aggregating instances,
			// so just show "Registered" or similar, maybe with Replicas count.
			state := fmt.Sprintf("REPLICAS:%d", spec.Replicas)

			// Columns mapping:
			// ID -> spec.Id
			// Type -> "global-spec" (Special type for filter)
			// CPU -> resources
			// Memory -> resources

			cpu := "0"
			mem := "0"
			if spec.Resources != nil {
				cpu = fmt.Sprintf("%.1f", spec.Resources.Cpu)
				mem = fmt.Sprintf("%d", spec.Resources.Memory)
			}

			statuses = append(statuses, tui.AgentStatus{
				ID:     spec.Id,
				Type:   "global-spec",
				State:  state,
				CPU:    cpu,
				Memory: mem,
			})
		}
	}

	return statuses, nil
}

// Lifecycle handles lifecycle operations for local agents
func (u *UnifiedController) Lifecycle(ctx context.Context, id, action string) (bool, error) {
	// Delegate to GAPI for local agents
	// For cluster nodes/jobs, return not supported
	return false, fmt.Errorf("lifecycle operations via unified TUI not yet implemented - use 'goblinctl agent lifecycle' commands")
}

// GetLogs streams logs from agents or cluster
func (u *UnifiedController) GetLogs(ctx context.Context, id string) (<-chan string, error) {
	ch := make(chan string, 100)
	go func() {
		defer close(ch)

		client, err := NewQUICRPCClient(u.rpcAddr, transport.TLSConfig{CAFile: tlsCA, InsecureSkipVerify: tlsInsecure})
		if err != nil {
			ch <- fmt.Sprintf("[ERROR] Failed to connect to QUIC RPC: %v", err)
			return
		}
		// Goroutine terminus: no caller to propagate to, so log.
		defer func() {
			if cerr := client.Close(); cerr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelWarn, "close rpc client failed", logattr.Err(cerr))
			}
		}()

		ch <- "[CLUSTER] Connected to cluster event stream (QUIC)"
		ch <- "--- Initial Cluster State ---"

		// 1. Dump Initial State (Context only)
		var members []supervisor.MemberInfo // Reusing struct from supervisor if available or defining local
		// Note: MemberInfo is in supervisor/scheduler_rpc.go
		if err := client.Call("SchedulerRPC.Members", struct{}{}, &members); err == nil {
			for _, m := range members {
				role := "follower"
				if m.Leader {
					role = "leader"
				}
				ch <- fmt.Sprintf("[MEMBER] %s (%s) - %s", m.Name, m.Status, role)
			}
		}

		var jobs []supervisor.JobInfo
		if err := client.Call("SchedulerRPC.ListJobs", struct{}{}, &jobs); err == nil {
			for _, j := range jobs {
				ch <- fmt.Sprintf("[JOB] %s: %s (on %s)", j.JobID, j.Status, j.AssignedNode)
			}
		}

		ch <- "-----------------------------"
		ch <- "--- Event Stream ---"

		// 2. Poll for Events (Cursor-based)
		var lastCursor uint64 = 0

		// Initial fetch to get history buffer (cursor 0)
		// Then poll continuously
		timer := time.NewTimer(0) // Start immediately
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				ch <- "[CLUSTER] Log stream closed"
				return
			case <-timer.C:
				req := supervisor.GetEventsRequest{Cursor: lastCursor}
				var events []supervisor.LogEvent

				if err := client.Call("SchedulerRPC.GetEvents", &req, &events); err != nil {
					ch <- fmt.Sprintf("[ERROR] Failed to fetch events: %v", err)
				} else {
					for _, event := range events {
						// Format: [HH:MM:SS.000] Message
						timestamp := event.Timestamp.Format("15:04:05.000")
						ch <- fmt.Sprintf("[%s] %s", timestamp, event.Message)

						if event.Index > lastCursor {
							lastCursor = event.Index
						}
					}
				}

				timer.Reset(1 * time.Second)
			}
		}
	}()

	return ch, nil
}
