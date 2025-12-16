package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/goppydae/gapi/core/tui"
	"github.com/goppydae/goblin/internal/supervisor"
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
func (u *UnifiedController) FetchStatus(ctx context.Context) ([]tui.AgentStatus, error) {
	var statuses []tui.AgentStatus

	// Connect to QUIC RPC
	client, err := NewQUICRPCClient(u.rpcAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to QUIC RPC: %w", err)
	}
	defer client.Close()

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

	// TODO: Section 3: Local agents via GAPI (not yet implemented)
	// This can be done by mounting GAPI CLI commands or creating a local controller

	return statuses, nil
}

// Lifecycle handles lifecycle operations for local agents
func (u *UnifiedController) Lifecycle(ctx context.Context, id, action string) (bool, error) {
	// Delegate to GAPI for local agents
	// For cluster nodes/jobs, return not supported
	return false, fmt.Errorf("lifecycle operations via unified TUI not yet implemented - use 'goblinctl agent' commands")
}

// GetLogs streams logs from agents or cluster
func (u *UnifiedController) GetLogs(ctx context.Context, id string) (<-chan string, error) {
	ch := make(chan string, 100) // Buffered channel

	// For now, implement basic cluster event streaming
	// Future: detect if id is agent or cluster entity

	go func() {
		defer close(ch)

		// Connect to QUIC RPC (using the same address as FetchStatus)
		client, err := NewQUICRPCClient(u.rpcAddr)
		if err != nil {
			ch <- fmt.Sprintf("[ERROR] Failed to connect to QUIC RPC: %v", err)
			return
		}
		defer client.Close()

		// Stream cluster membership events
		ch <- "[CLUSTER] Connected to cluster event stream (QUIC)"

		// Poll for member changes every 2 seconds
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		var lastMemberCount int
		// Initial fetch to set baseline
		type MemberShort struct {
			Name   string
			Status string
		}
		var members []MemberShort
		if err := client.Call("SchedulerRPC.Members", struct{}{}, &members); err == nil {
			lastMemberCount = len(members)
		}

		for {
			select {
			case <-ctx.Done():
				ch <- "[CLUSTER] Log stream closed"
				return
			case <-ticker.C:
				var currentMembers []MemberShort
				if err := client.Call("SchedulerRPC.Members", struct{}{}, &currentMembers); err != nil {
					ch <- fmt.Sprintf("[ERROR] Failed to fetch members: %v", err)
					continue
				}

				if len(currentMembers) != lastMemberCount {
					ch <- fmt.Sprintf("[CLUSTER] Member count changed: %d nodes", len(currentMembers))
					lastMemberCount = len(currentMembers)

					// List changes
					for _, m := range currentMembers {
						ch <- fmt.Sprintf("[CLUSTER] Member Node: %s (%s)", m.Name, m.Status)
					}
				}

				// Also check for job updates
				var jobs []supervisor.JobInfo
				if err := client.Call("SchedulerRPC.ListJobs", struct{}{}, &jobs); err == nil {
					// Simple summary for now to avoid spamming logs
					// In a real implementation we would track diffs
					if len(jobs) > 0 {
						// Only log occasionally or on change - for now just debug
						// ch <- fmt.Sprintf("[CLUSTER] %d jobs running", len(jobs))
					}
				}
			}
		}
	}()

	return ch, nil
}
