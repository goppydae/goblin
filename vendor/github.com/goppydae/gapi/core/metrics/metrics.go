package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is a dedicated registry for all GAPI metrics. Registering against it
// (rather than prometheus.DefaultRegisterer) means host applications that embed
// GAPI and replace the default registry still expose GAPI metrics via Handler().
var Registry = prometheus.NewRegistry()

// factory registers every metric below against Registry.
var factory = promauto.With(Registry)

// Handler returns an http.Handler that serves the GAPI metric Registry.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}

// The gapi_ metric NAMES below are deliberately not product-namespaced,
// and this is an exclusion rather than an oversight (GAPI-DIV-061).
//
// A metric name is scraped: it is referenced by dashboards, recording
// rules and alerts that live outside this repo, so renaming one breaks
// consumers exactly the way renaming an ALPN or a protobuf package
// would. They belong to the WIRE class. The HELP text next to them does
// not - nobody alerts on help text - which is why the two lines that
// spelled the vendor there now name the role instead.
var (
	// Build Info
	BuildInfo = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_build_info",
			Help: "Supervisor build information",
		},
		[]string{"version", "commit", "go_version"},
	)

	// Supervisor Metrics
	SupervisorUptime = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "gapi_supervisor_uptime_seconds",
			Help: "Supervisor uptime in seconds",
		},
	)

	AgentsTotal = factory.NewGauge(
		prometheus.GaugeOpts{
			Name: "gapi_supervisor_agents_total",
			Help: "Total number of registered agents",
		},
	)

	// Agent Lifecycle Metrics
	AgentState = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_state",
			Help: "Current state of agents (1 = in this state, 0 = not)",
		},
		[]string{"id", "type", "state"},
	)

	AgentStarts = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_starts_total",
			Help: "Total number of agent starts",
		},
		[]string{"id", "type"},
	)

	AgentStops = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_stops_total",
			Help: "Total number of agent stops",
		},
		[]string{"id", "type"},
	)

	AgentFailures = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_agent_failures_total",
			Help: "Total number of agent failures",
		},
		[]string{"id", "type", "reason"},
	)

	// Resource Usage Metrics
	AgentCPUUsage = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_cpu_usage",
			Help: "Agent CPU usage percentage",
		},
		[]string{"id", "type"},
	)

	AgentMemoryBytes = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_memory_bytes",
			Help: "Agent memory usage in bytes",
		},
		[]string{"id", "type"},
	)

	AgentUptimeSeconds = factory.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gapi_agent_uptime_seconds",
			Help: "Agent uptime in seconds",
		},
		[]string{"id", "type"},
	)

	// EventBus Metrics
	EventBusEvents = factory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gapi_eventbus_events_total",
			Help: "Total number of events published",
		},
		[]string{"topic"},
	)

	EventBusSubscribers = factory.NewGaugeVec(
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
