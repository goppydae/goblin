package eventbus

import (
	"errors"
	"fmt"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// clusterEventName is the Serf user-event name used to gossip cluster
// events. HandleSerfEvent's filter and PublishCluster's send site must
// agree on this exact string, so it lives in one place.
const clusterEventName = "goblin.event"

// serfUserEventLimit mirrors serf's default UserEventSizeLimit (see
// vendor/github.com/hashicorp/serf/serf/config.go and serf.go's
// Serf.UserEvent): the event name and the payload are counted together,
// before serf's own msgpack encoding adds further overhead. Checking
// here lets an oversized event fail with the eventbus's own error and
// context, before the bytes ever reach serf.
const serfUserEventLimit = 512

// ErrClusterEventOversized indicates a marshalled ClusterEvent would not
// fit within serf's user-event size limit.
var ErrClusterEventOversized = errors.New("cluster event exceeds serf user-event size limit")

// eventToWire converts an in-process Event to its gossip/Raft wire form.
// Payload conversion can fail if it holds a value google.protobuf.Struct
// cannot represent (channels, funcs, complex numbers, ...); the error
// propagates rather than silently dropping the event or the field.
func eventToWire(e Event) (*goblinv1.ClusterEvent, error) {
	var payload *structpb.Struct
	if e.Payload != nil {
		p, err := structpb.NewStruct(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("eventbus: converting event payload to struct: %w", err)
		}
		payload = p
	}

	return &goblinv1.ClusterEvent{
		Id:        e.ID,
		Topic:     e.Topic,
		Namespace: e.Namespace,
		Tags:      e.Tags,
		Payload:   payload,
		NodeId:    e.NodeID,
		Timestamp: timestamppb.New(e.Timestamp),
	}, nil
}

// eventFromWire converts a gossip/Raft wire ClusterEvent back to the
// in-process Event. Nil Payload/Timestamp (e.g. a zero-value message, or
// one built by hand rather than via eventToWire) leave the corresponding
// Event field at its zero value instead of panicking.
func eventFromWire(ce *goblinv1.ClusterEvent) Event {
	event := Event{
		ID:        ce.GetId(),
		Topic:     ce.GetTopic(),
		Namespace: ce.GetNamespace(),
		Tags:      ce.GetTags(),
		NodeID:    ce.GetNodeId(),
	}
	if ce.GetPayload() != nil {
		event.Payload = ce.GetPayload().AsMap()
	}
	if ce.GetTimestamp() != nil {
		event.Timestamp = ce.GetTimestamp().AsTime()
	}
	return event
}

// checkSerfEventSize enforces serfUserEventLimit before the caller hands
// a marshalled event to serf: name and payload are counted together, the
// same accounting serf.Serf.UserEvent uses internally, so this rejects
// exactly what serf would reject - just with the eventbus's own error
// and enough detail (size, limit) to debug without reading serf source.
func checkSerfEventSize(name string, payload []byte) error {
	n := len(name) + len(payload)
	if n > serfUserEventLimit {
		return fmt.Errorf("%w: %d bytes (name %d + payload %d) exceeds limit of %d bytes",
			ErrClusterEventOversized, n, len(name), len(payload), serfUserEventLimit)
	}
	return nil
}
