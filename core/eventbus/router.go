package eventbus

import (
	"strings"
)

// RoutingStrategy determines how events are distributed
type RoutingStrategy int

const (
	// RouteLocal - Event stays on local node only
	RouteLocal RoutingStrategy = iota
	// RouteGossip - Event is gossiped to all cluster nodes
	RouteGossip
	// RouteRaft - Event requires Raft consensus (leader only)
	RouteRaft
	// RouteDirect - Event sent directly to specific node
	RouteDirect
)

// EventRouter determines routing strategy based on topic
type EventRouter struct {
	rules map[string]RoutingStrategy
}

// NewEventRouter creates a new event router with default rules
func NewEventRouter() *EventRouter {
	router := &EventRouter{
		rules: make(map[string]RoutingStrategy),
	}

	// Define default routing rules
	router.AddRule("agent.", RouteLocal)    // Agent lifecycle stays local
	router.AddRule("cluster.", RouteGossip) // Cluster events gossip
	router.AddRule("consensus.", RouteRaft) // Consensus requires Raft
	router.AddRule("global.", RouteGossip)  // Global events gossip
	router.AddRule("node.", RouteLocal)     // Node-specific stays local

	return router
}

// AddRule adds a routing rule for a topic prefix
func (r *EventRouter) AddRule(topicPrefix string, strategy RoutingStrategy) {
	r.rules[topicPrefix] = strategy
}

// GetStrategy returns the routing strategy for a given topic
func (r *EventRouter) GetStrategy(topic string) RoutingStrategy {
	// Check for exact prefix matches
	for prefix, strategy := range r.rules {
		if strings.HasPrefix(topic, prefix) {
			return strategy
		}
	}

	// Default to local if no rule matches
	return RouteLocal
}

// ShouldReplicate returns true if the event should be sent to other nodes
func (r *EventRouter) ShouldReplicate(topic string) bool {
	strategy := r.GetStrategy(topic)
	return strategy == RouteGossip || strategy == RouteRaft
}

// RequiresLeader returns true if only the leader can publish this event
func (r *EventRouter) RequiresLeader(topic string) bool {
	return r.GetStrategy(topic) == RouteRaft
}
