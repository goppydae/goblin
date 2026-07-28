package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/goppydae/gapi/core/cgroups"
	"github.com/goppydae/gapi/core/metrics"
	"github.com/goppydae/gapi/internal/logattr"
)

// collectMetrics gathers resource usage metrics from all agents
func (s *Supervisor) collectMetrics(startTime time.Time) {
	// Update supervisor uptime
	uptime := s.clock.Since(startTime).Seconds()
	metrics.SupervisorUptime.Set(uptime)

	// Get all registered agents
	entries, err := s.registry.List()
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "failed to list agents for metrics collection", logattr.Err(err))
		return
	}

	// Update total agent count
	metrics.AgentsTotal.Set(float64(len(entries)))

	s.logger.LogAttrs(context.Background(), slog.LevelDebug, "collecting metrics",
		slog.Int("agent_count", len(entries)), slog.Duration("uptime", s.clock.Since(startTime)))

	// Collect per-agent metrics
	for _, entry := range entries {
		agentType := entry.Type
		agentID := entry.ID

		// Get agent from manager to check if it's running
		ag := s.manager.Get(agentID)
		if ag == nil {
			continue
		}

		// Update state metrics
		currentState := ag.Controller().State()
		// Clear all states for this agent first
		for _, state := range []string{"initializing", "initialized", "starting", "running", "stopping", "stopped", "reloading", "error"} {
			metrics.AgentState.WithLabelValues(agentID, agentType, state).Set(0)
		}
		// Set current state to 1
		metrics.AgentState.WithLabelValues(agentID, agentType, currentState).Set(1)

		// Collect resource metrics from cgroups
		cgName := fmt.Sprintf("gapid-%s", agentID)
		if stats, err := cgroups.GetStats(cgName); err == nil {
			// Calculate uptime (placeholder - would need process start time tracking)
			// For now, just use 0 if not available
			uptimeSeconds := 0.0

			memBytes := uint64(0)
			if stats.MemoryUsage > 0 {
				memBytes = uint64(stats.MemoryUsage)
			}
			metrics.UpdateAgentResources(agentID, agentType, stats.CPUUsage, memBytes, uptimeSeconds)
		}
	}
}
