package eventbus

import (
	"sync"
	"testing"
	"time"
)

func TestDistributedEventBus_PublishLocal(t *testing.T) {
	bus := NewDistributedEventBus("node1", nil, nil)

	received := false
	var receivedEvent Event
	var mu sync.Mutex

	bus.Subscribe("test.topic", func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		received = true
		receivedEvent = e
	})

	payload := map[string]interface{}{
		"message": "hello",
		"count":   42,
	}

	err := bus.PublishLocal("", "test.topic", payload, nil)
	if err != nil {
		t.Fatalf("PublishLocal failed: %v", err)
	}

	// Wait for async handler
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if !received {
		t.Fatal("Event was not received")
	}

	if receivedEvent.Topic != "test.topic" {
		t.Errorf("Expected topic 'test.topic', got '%s'", receivedEvent.Topic)
	}

	if receivedEvent.NodeID != "node1" {
		t.Errorf("Expected nodeID 'node1', got '%s'", receivedEvent.NodeID)
	}

	if receivedEvent.Payload["message"] != "hello" {
		t.Errorf("Expected message 'hello', got '%v'", receivedEvent.Payload["message"])
	}
}

func TestDistributedEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewDistributedEventBus("node1", nil, nil)

	count := 0
	var mu sync.Mutex

	// Add multiple subscribers
	for i := 0; i < 3; i++ {
		bus.Subscribe("test.multi", func(e Event) {
			mu.Lock()
			defer mu.Unlock()
			count++
		})
	}

	payload := map[string]interface{}{"test": true}
	err := bus.PublishLocal("", "test.multi", payload, nil)
	if err != nil {
		t.Fatalf("PublishLocal failed: %v", err)
	}

	// Wait for async handlers
	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if count != 3 {
		t.Errorf("Expected 3 handlers to be called, got %d", count)
	}
}

func TestDistributedEventBus_NoSubscribers(t *testing.T) {
	bus := NewDistributedEventBus("node1", nil, nil)

	payload := map[string]interface{}{"test": true}
	err := bus.PublishLocal("", "nonexistent.topic", payload, nil)
	if err != nil {
		t.Fatalf("PublishLocal should not error with no subscribers: %v", err)
	}
}

func TestEventRouter_GetStrategy(t *testing.T) {
	router := NewEventRouter()

	tests := []struct {
		topic    string
		expected RoutingStrategy
	}{
		{"agent.started", RouteLocal},
		{"agent.stopped", RouteLocal},
		{"cluster.node.joined", RouteGossip},
		{"cluster.node.left", RouteGossip},
		{"consensus.leader.elected", RouteRaft},
		{"global.alert", RouteGossip},
		{"node.metrics", RouteLocal},
		{"unknown.topic", RouteLocal}, // Default
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			strategy := router.GetStrategy(tt.topic)
			if strategy != tt.expected {
				t.Errorf("Expected strategy %v for topic '%s', got %v",
					tt.expected, tt.topic, strategy)
			}
		})
	}
}

func TestEventRouter_ShouldReplicate(t *testing.T) {
	router := NewEventRouter()

	tests := []struct {
		topic    string
		expected bool
	}{
		{"agent.started", false},     // Local only
		{"cluster.update", true},     // Gossip
		{"consensus.decision", true}, // Raft
		{"global.alert", true},       // Gossip
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			result := router.ShouldReplicate(tt.topic)
			if result != tt.expected {
				t.Errorf("Expected ShouldReplicate=%v for topic '%s', got %v",
					tt.expected, tt.topic, result)
			}
		})
	}
}

func TestEventRouter_RequiresLeader(t *testing.T) {
	router := NewEventRouter()

	tests := []struct {
		topic    string
		expected bool
	}{
		{"agent.started", false},
		{"cluster.update", false},
		{"consensus.decision", true}, // Only Raft requires leader
		{"global.alert", false},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			result := router.RequiresLeader(tt.topic)
			if result != tt.expected {
				t.Errorf("Expected RequiresLeader=%v for topic '%s', got %v",
					tt.expected, tt.topic, result)
			}
		})
	}
}
