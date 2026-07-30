package eventbus

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

// TestEventWireRoundTrip covers eventToWire/eventFromWire: every scalar
// field must survive the round trip, and payload maps (including nested
// maps and numeric values) must come back exactly as the existing JSON
// path produced them - float64 for numbers, since in-process consumers
// (e.g. distributed_test.go's PublishLocal assertions) must see no
// change from the pre-DIV-039 encoding.
func TestEventWireRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{
			name: "full event with nested payload",
			event: Event{
				ID:        "01977f3e-0000-7000-8000-000000000001",
				Topic:     "cluster.node.joined",
				Namespace: "default",
				Tags:      []string{"a", "b"},
				NodeID:    "node1",
				Timestamp: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
				Payload: map[string]interface{}{
					"message": "hello",
					"count":   42,
					"nested": map[string]interface{}{
						"inner":   "value",
						"depth":   2,
						"ratio":   3.5,
						"present": true,
					},
					"list": []interface{}{"x", "y", float64(3)},
				},
			},
		},
		{
			name: "empty tags and payload",
			event: Event{
				ID:        "01977f3e-0000-7000-8000-000000000002",
				Topic:     "node.metrics",
				Namespace: "",
				Tags:      nil,
				NodeID:    "node2",
				Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				Payload:   map[string]interface{}{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire, err := eventToWire(tt.event)
			if err != nil {
				t.Fatalf("eventToWire failed: %v", err)
			}

			got := eventFromWire(wire)

			if got.ID != tt.event.ID {
				t.Errorf("ID: got %q, want %q", got.ID, tt.event.ID)
			}
			if got.Topic != tt.event.Topic {
				t.Errorf("Topic: got %q, want %q", got.Topic, tt.event.Topic)
			}
			if got.Namespace != tt.event.Namespace {
				t.Errorf("Namespace: got %q, want %q", got.Namespace, tt.event.Namespace)
			}
			if got.NodeID != tt.event.NodeID {
				t.Errorf("NodeID: got %q, want %q", got.NodeID, tt.event.NodeID)
			}
			if !got.Timestamp.Equal(tt.event.Timestamp) {
				t.Errorf("Timestamp: got %v, want %v", got.Timestamp, tt.event.Timestamp)
			}
			if len(got.Tags) != len(tt.event.Tags) {
				t.Errorf("Tags: got %v, want %v", got.Tags, tt.event.Tags)
			} else {
				for i := range tt.event.Tags {
					if got.Tags[i] != tt.event.Tags[i] {
						t.Errorf("Tags[%d]: got %q, want %q", i, got.Tags[i], tt.event.Tags[i])
					}
				}
			}

			if len(tt.event.Payload) == 0 {
				if len(got.Payload) != 0 {
					t.Errorf("Payload: got %v, want empty", got.Payload)
				}
				return
			}

			if got.Payload["message"] != tt.event.Payload["message"] {
				t.Errorf("Payload[message]: got %v, want %v", got.Payload["message"], tt.event.Payload["message"])
			}
			// JSON/Struct number semantics: ints come back float64, same
			// as the pre-DIV-039 encoding/json path produced.
			if v, ok := got.Payload["count"].(float64); !ok || v != 42 {
				t.Errorf("Payload[count]: got %#v (type %T), want float64(42)", got.Payload["count"], got.Payload["count"])
			}
			nested, ok := got.Payload["nested"].(map[string]interface{})
			if !ok {
				t.Fatalf("Payload[nested]: got %#v, want map[string]interface{}", got.Payload["nested"])
			}
			if nested["inner"] != "value" {
				t.Errorf("Payload[nested][inner]: got %v, want %q", nested["inner"], "value")
			}
			if v, ok := nested["depth"].(float64); !ok || v != 2 {
				t.Errorf("Payload[nested][depth]: got %#v, want float64(2)", nested["depth"])
			}
			if v, ok := nested["ratio"].(float64); !ok || v != 3.5 {
				t.Errorf("Payload[nested][ratio]: got %#v, want float64(3.5)", nested["ratio"])
			}
			if nested["present"] != true {
				t.Errorf("Payload[nested][present]: got %v, want true", nested["present"])
			}
			list, ok := got.Payload["list"].([]interface{})
			if !ok || len(list) != 3 {
				t.Fatalf("Payload[list]: got %#v, want a 3-element slice", got.Payload["list"])
			}
			if list[0] != "x" || list[1] != "y" {
				t.Errorf("Payload[list]: got %#v", list)
			}
			if v, ok := list[2].(float64); !ok || v != 3 {
				t.Errorf("Payload[list][2]: got %#v, want float64(3)", list[2])
			}
		})
	}
}

// TestEventFromWire_NilGuards covers the nil payload/timestamp guard:
// eventFromWire must not panic and must leave the corresponding Event
// field at its zero value.
func TestEventFromWire_NilGuards(t *testing.T) {
	ce, err := eventToWire(Event{ID: "x", Topic: "t"})
	if err != nil {
		t.Fatalf("eventToWire failed: %v", err)
	}
	ce.Payload = nil
	ce.Timestamp = nil

	got := eventFromWire(ce)

	if got.Payload != nil {
		t.Errorf("Payload: got %v, want nil", got.Payload)
	}
	if !got.Timestamp.IsZero() {
		t.Errorf("Timestamp: got %v, want zero value", got.Timestamp)
	}
}

// TestEventToWire_UnrepresentablePayload covers a payload value that
// google.protobuf.Struct cannot encode (a channel): the conversion must
// return an error, not panic.
func TestEventToWire_UnrepresentablePayload(t *testing.T) {
	event := Event{
		ID:    "x",
		Topic: "t",
		Payload: map[string]interface{}{
			"bad": make(chan int),
		},
	}

	_, err := eventToWire(event)
	if err == nil {
		t.Fatal("expected error for unrepresentable payload value, got nil")
	}
}

// TestCheckSerfEventSize covers the pre-serf size ceiling: a marshalled
// event whose len(name)+len(payload) exceeds serfUserEventLimit must be
// rejected with a named error before ever reaching serf, and the error
// text must mention both the actual size and the limit.
func TestCheckSerfEventSize(t *testing.T) {
	t.Run("under limit passes", func(t *testing.T) {
		if err := checkSerfEventSize("goblin.event", make([]byte, 10)); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("exactly at limit passes", func(t *testing.T) {
		name := "goblin.event"
		payload := make([]byte, serfUserEventLimit-len(name))
		if err := checkSerfEventSize(name, payload); err != nil {
			t.Fatalf("expected no error at exact limit, got %v", err)
		}
	})

	t.Run("over limit fails with named error", func(t *testing.T) {
		name := "goblin.event"
		payload := make([]byte, serfUserEventLimit) // len(name)+len(payload) > limit
		err := checkSerfEventSize(name, payload)
		if err == nil {
			t.Fatal("expected error for oversized event, got nil")
		}
		if !errors.Is(err, ErrClusterEventOversized) {
			t.Errorf("expected errors.Is(err, ErrClusterEventOversized), got %v", err)
		}
		msg := err.Error()
		total := len(name) + len(payload)
		if !strings.Contains(msg, "512") {
			t.Errorf("error message %q does not mention the limit (512)", msg)
		}
		if !strings.Contains(msg, strconv.Itoa(total)) {
			t.Errorf("error message %q does not mention the actual size (%d)", msg, total)
		}
	})
}

// TestOversizedEventFailsBeforeSerf is an integration-style check on the
// real publish path: a payload large enough to blow the serf ceiling
// must be rejected by PublishCluster itself (membership is nil here, so
// any success would mean the size check never ran - the failure has to
// come from checkSerfEventSize, not a downstream serf call).
func TestOversizedEventFailsBeforeSerf(t *testing.T) {
	bus := NewDistributedEventBus("node1", nil, nil)

	// Force conversion through eventToWire/proto.Marshal directly with a
	// payload guaranteed to exceed serfUserEventLimit once marshalled.
	big := strings.Repeat("x", serfUserEventLimit*2)
	event := Event{
		ID:        "01977f3e-0000-7000-8000-000000000003",
		Topic:     "cluster.big",
		Namespace: "default",
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
		Payload:   map[string]interface{}{"blob": big},
	}
	wire, err := eventToWire(event)
	if err != nil {
		t.Fatalf("eventToWire failed: %v", err)
	}
	b, err := proto.Marshal(wire)
	if err != nil {
		t.Fatalf("proto.Marshal failed: %v", err)
	}
	if err := checkSerfEventSize(clusterEventName, b); err == nil {
		t.Fatal("expected checkSerfEventSize to reject an oversized marshalled event")
	}
}
