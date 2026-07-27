package store

import (
	"context"
	"testing"
	"time"

	"github.com/goppydae/goblin/core/eventbus"
)

func TestStore_Watch(t *testing.T) {
	// Use real EventBus
	bus := eventbus.NewDistributedEventBus("test_node", nil, nil)

	// Create Store with nil consensus (not used for Watch in this test as we manually publish)
	store := NewStore(nil, bus)

	ctx, cancel := context.WithCancel(context.Background())
	// Defer cancel just in case, but we call it explicitly later
	defer cancel()

	// 1. Start Watch
	ch := store.Watch(ctx, "test_ns", "key1")

	// 2. Publish event (simulating Set)
	payload := map[string]interface{}{
		"op":        "set",
		"namespace": "test_ns",
		"key":       "key1",
		"value":     "val1",
	}

	go func() {
		// Give subscription time to register
		time.Sleep(50 * time.Millisecond)
		if err := bus.PublishLocal("test_ns", "store.change", payload, nil); err != nil {
			t.Errorf("PublishLocal: %v", err)
		}
	}()

	// 3. Verify event received
	select {
	case e := <-ch:
		if e.Payload["key"] != "key1" {
			t.Errorf("Expected key 'key1', got %v", e.Payload["key"])
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for event")
	}

	// 4. Test filtering
	// Publish event for different key
	if err := bus.PublishLocal("test_ns", "store.change", map[string]interface{}{
		"op":        "set",
		"namespace": "test_ns",
		"key":       "key2",
		"value":     "val2",
	}, nil); err != nil {
		t.Fatalf("PublishLocal: %v", err)
	}

	select {
	case e := <-ch:
		t.Errorf("Should not have received event for key2: %v", e)
	case <-time.After(50 * time.Millisecond):
		// Expected
	}

	// 5. Cancel context and verify close
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Channel should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Timeout waiting for channel close")
	}
}
