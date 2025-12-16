package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Build Info
	BuildInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_build_info",
			Help: "GAPI build information",
		},
		[]string{"version", "commit", "go_version"},
	)

	// Supervisor Metrics
	SupervisorUptime = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "gapi_supervisor_uptime_seconds",
			Help: "GAPI supervisor uptime in seconds",
		},
	)

	AgentsTotal = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "gapi_supervisor_agents_total",
			Help: "Total number of registered agents",
		},
	)

	// Agent Lifecycle Metrics
	AgentState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_state",
			Help: "Current state of agents (1 = in this state, 0 = not)",
		},
		[]string{"id", "type", "state"},
	)

	AgentStarts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_starts_total",
			Help: "Total number of agent starts",
		},
		[]string{"id", "type"},
	)

	AgentStops = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_stops_total",
			Help: "Total number of agent stops",
		},
		[]string{"id", "type"},
	)

	AgentFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_failures_total",
			Help: "Total number of agent failures",
		},
		[]string{"id", "type", "reason"},
	)

	// Resource Usage Metrics
	AgentCPUUsage = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_cpu_usage",
			Help: "Agent CPU usage percentage",
		},
		[]string{"id", "type"},
	)

	AgentMemoryBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_memory_bytes",
			Help: "Agent memory usage in bytes",
		},
		[]string{"id", "type"},
	)

	AgentUptimeSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_uptime_seconds",
			Help: "Agent uptime in seconds",
		},
		[]string{"id", "type"},
	)

	// EventBus Metrics
	EventBusEvents = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_eventbus_events_total",
			Help: "Total number of events published",
		},
		[]string{"topic"},
	)

	EventBusSubscribers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_eventbus_subscribers",
			Help: "Number of active subscribers per topic",
		},
		[]string{"topic"},
	)
)

// RecordAgentStateChange updates agent state metrics
func RecordAgentStateChange(id, agentType, oldState, newState string) {
	// Clear old state
	if oldState != "" {
		AgentState.WithLabelValues(id, agentType, oldState).Set(0)
	}
	// Set new state
	AgentState.WithLabelValues(id, agentType, newState).Set(1)
}

// RecordAgentStart increments agent start counter
func RecordAgentStart(id, agentType string) {
	AgentStarts.WithLabelValues(id, agentType).Inc()
}

// RecordAgentStop increments agent stop counter
func RecordAgentStop(id, agentType string) {
	AgentStops.WithLabelValues(id, agentType).Inc()
}

// RecordAgentFailure increments agent failure counter
func RecordAgentFailure(id, agentType, reason string) {
	AgentFailures.WithLabelValues(id, agentType, reason).Inc()
}

// UpdateAgentResources updates agent resource usage metrics
func UpdateAgentResources(id, agentType string, cpuUsage float64, memoryBytes uint64, uptimeSeconds float64) {
	if cpuUsage > 0 {
		AgentCPUUsage.WithLabelValues(id, agentType).Set(cpuUsage)
	}
	if memoryBytes > 0 {
		AgentMemoryBytes.WithLabelValues(id, agentType).Set(float64(memoryBytes))
	}
	if uptimeSeconds > 0 {
		AgentUptimeSeconds.WithLabelValues(id, agentType).Set(uptimeSeconds)
	}
}

// RecordEvent increments event counter for a topic
func RecordEvent(topic string) {
	EventBusEvents.WithLabelValues(topic).Inc()
}

// UpdateSubscriberCount updates subscriber count for a topic
func UpdateSubscriberCount(topic string, count int) {
	EventBusSubscribers.WithLabelValues(topic).Set(float64(count))
}
