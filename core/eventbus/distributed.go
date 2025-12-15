package eventbus

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
)

// Event represents a distributed event
type Event struct {
	ID        string                 `json:"id"`
	Topic     string                 `json:"topic"`
	Payload   map[string]interface{} `json:"payload"`
	NodeID    string                 `json:"node_id"`
	Timestamp time.Time              `json:"timestamp"`
}

// EventHandler is a function that handles events
type EventHandler func(Event)

// DistributedEventBus manages cluster-wide event distribution
type DistributedEventBus struct {
	nodeID      string
	subscribers map[string][]EventHandler
	router      *EventRouter
	membership  *cluster.Membership
	consensus   *consensus.Consensus
	mu          sync.RWMutex
}

// NewDistributedEventBus creates a new distributed event bus
func NewDistributedEventBus(nodeID string, membership *cluster.Membership, consensus *consensus.Consensus) *DistributedEventBus {
	bus := &DistributedEventBus{
		nodeID:      nodeID,
		subscribers: make(map[string][]EventHandler),
		router:      NewEventRouter(),
		membership:  membership,
		consensus:   consensus,
	}

	// Start listening for Serf user events if membership is available
	if membership != nil {
		go bus.listenSerfEvents()
	}

	return bus
}

// Subscribe registers a handler for a specific topic
func (bus *DistributedEventBus) Subscribe(topic string, handler EventHandler) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.subscribers[topic] = append(bus.subscribers[topic], handler)
}

// PublishLocal publishes an event only on the local node
func (bus *DistributedEventBus) PublishLocal(topic string, payload map[string]interface{}) error {
	event := Event{
		ID:        generateEventID(),
		Topic:     topic,
		Payload:   payload,
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
	}

	return bus.dispatch(event)
}

// PublishCluster publishes an event to all nodes in the cluster via Serf gossip
func (bus *DistributedEventBus) PublishCluster(topic string, payload map[string]interface{}) error {
	event := Event{
		ID:        generateEventID(),
		Topic:     topic,
		Payload:   payload,
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
	}

	// Dispatch locally first
	if err := bus.dispatch(event); err != nil {
		return err
	}

	// Replicate via Serf if available
	if bus.membership != nil {
		eventJSON, err := json.Marshal(event)
		if err != nil {
			return err
		}

		return bus.membership.UserEvent("goblin.event", eventJSON)
	}

	return nil
}

// PublishLeader publishes an event that only the leader can send via Raft
func (bus *DistributedEventBus) PublishLeader(topic string, payload map[string]interface{}) error {
	// Check if we have consensus
	if bus.consensus == nil {
		return errors.New("consensus not available")
	}

	// Check if we're the leader
	if !bus.consensus.IsLeader() {
		return errors.New("not leader")
	}

	event := Event{
		ID:        generateEventID(),
		Topic:     topic,
		Payload:   payload,
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
	}

	// Apply via Raft
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return err
	}

	return bus.consensus.Apply(eventJSON, 5*time.Second)
}

// dispatch sends an event to all registered handlers
func (bus *DistributedEventBus) dispatch(event Event) error {
	bus.mu.RLock()
	handlers, exists := bus.subscribers[event.Topic]
	bus.mu.RUnlock()

	if !exists {
		return nil // No subscribers, not an error
	}

	// Call handlers asynchronously
	for _, handler := range handlers {
		go handler(event)
	}

	return nil
}

// HandleRemoteEvent processes an event received from another node
func (bus *DistributedEventBus) HandleRemoteEvent(eventJSON []byte) error {
	var event Event
	if err := json.Unmarshal(eventJSON, &event); err != nil {
		return err
	}

	return bus.dispatch(event)
}

// listenSerfEvents listens for Serf user events and dispatches them
func (bus *DistributedEventBus) listenSerfEvents() {
	// This would integrate with Serf's event channel
	// For now, it's a placeholder for the integration point
}

// generateEventID creates a unique event ID
func generateEventID() string {
	return time.Now().Format("20060102150405.000000")
}
