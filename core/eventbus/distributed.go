// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package eventbus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/internal/ident"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
	"google.golang.org/protobuf/proto"
)

// Event represents a distributed event
type Event struct {
	ID        string                 `json:"id"`
	Topic     string                 `json:"topic"`
	Namespace string                 `json:"namespace"`
	Tags      []string               `json:"tags"`
	Payload   map[string]interface{} `json:"payload"`
	NodeID    string                 `json:"node_id"`
	Timestamp time.Time              `json:"timestamp"`
}

// EventHandler is a function that handles events
type EventHandler func(Event)

// EventBus defines the interface for the event bus
type EventBus interface {
	Subscribe(topic string, handler EventHandler) Subscription
	PublishLocal(namespace, topic string, payload map[string]interface{}, tags []string) error
	PublishCluster(namespace, topic string, payload map[string]interface{}, tags []string) error
	PublishLeader(namespace, topic string, payload map[string]interface{}, tags []string) error
}

// DistributedEventBus manages cluster-wide event distribution
type DistributedEventBus struct {
	nodeID      string
	subscribers map[string]map[string]EventHandler
	router      *EventRouter
	membership  *cluster.Membership
	consensus   *consensus.Consensus
	mu          sync.RWMutex
}

// NewDistributedEventBus creates a new distributed event bus
func NewDistributedEventBus(nodeID string, membership *cluster.Membership, consensus *consensus.Consensus) *DistributedEventBus {
	bus := &DistributedEventBus{
		nodeID:      nodeID,
		subscribers: make(map[string]map[string]EventHandler),
		router:      NewEventRouter(),
		membership:  membership,
		consensus:   consensus,
	}

	return bus
}

// HandleSerfEvent ingests a Serf event into the bus: goblin.event user
// events from other nodes are unmarshaled and dispatched to local
// subscribers. The supervisor's single Serf handler calls this - Serf
// supports one handler, so the bus cannot register its own (this closes
// the proto-2 'distributed eventbus integration point' seam).
func (bus *DistributedEventBus) HandleSerfEvent(e serf.Event) {
	ue, ok := e.(serf.UserEvent)
	if !ok || ue.Name != clusterEventName {
		return
	}
	var ce goblinv1.ClusterEvent
	if err := proto.Unmarshal(ue.Payload, &ce); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to unmarshal cluster event", logattr.Err(err))
		return
	}
	event := eventFromWire(&ce)
	// PublishCluster already dispatched locally on the origin node.
	if event.NodeID == bus.nodeID {
		return
	}
	if err := bus.dispatch(event); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to dispatch cluster event", logattr.Topic(event.Topic), logattr.Err(err))
	}
}

// Subscription represents an active event subscription
type Subscription struct {
	ID    string
	Topic string
	bus   *DistributedEventBus
}

// Unsubscribe removes the subscription
func (s *Subscription) Unsubscribe() {
	s.bus.Unsubscribe(s)
}

// Subscribe registers a handler for a specific topic
func (bus *DistributedEventBus) Subscribe(topic string, handler EventHandler) Subscription {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	subID := generateEventID() // Reusing ID generator for simplicity
	if _, ok := bus.subscribers[topic]; !ok {
		bus.subscribers[topic] = make(map[string]EventHandler)
	}
	bus.subscribers[topic][subID] = handler

	return Subscription{
		ID:    subID,
		Topic: topic,
		bus:   bus,
	}
}

// Unsubscribe removes a handler
func (bus *DistributedEventBus) Unsubscribe(sub *Subscription) {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	if handlers, ok := bus.subscribers[sub.Topic]; ok {
		delete(handlers, sub.ID)
		if len(handlers) == 0 {
			delete(bus.subscribers, sub.Topic)
		}
	}
}

// PublishLocal publishes an event only on the local node
func (bus *DistributedEventBus) PublishLocal(namespace, topic string, payload map[string]interface{}, tags []string) error {
	event := Event{
		ID:        generateEventID(),
		Topic:     topic,
		Namespace: namespace,
		Tags:      tags,
		Payload:   payload,
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
	}

	return bus.dispatch(event)
}

// PublishCluster publishes an event to all nodes in the cluster via Serf gossip
func (bus *DistributedEventBus) PublishCluster(namespace, topic string, payload map[string]interface{}, tags []string) error {
	event := Event{
		ID:        generateEventID(),
		Topic:     topic,
		Namespace: namespace,
		Tags:      tags,
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
		wire, err := eventToWire(event)
		if err != nil {
			return err
		}
		b, err := proto.Marshal(wire)
		if err != nil {
			return err
		}
		if err := checkSerfEventSize(clusterEventName, b); err != nil {
			return err
		}

		return bus.membership.UserEvent(clusterEventName, b)
	}

	return nil
}

// PublishLeader publishes an event that only the leader can send via Raft
func (bus *DistributedEventBus) PublishLeader(namespace, topic string, payload map[string]interface{}, tags []string) error {
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
		Namespace: namespace,
		Tags:      tags,
		Payload:   payload,
		NodeID:    bus.nodeID,
		Timestamp: time.Now(),
	}

	// Apply via Raft
	wire, err := eventToWire(event)
	if err != nil {
		return err
	}
	eventBytes, err := proto.Marshal(wire)
	if err != nil {
		return err
	}

	return bus.consensus.Apply(eventBytes, 5*time.Second)
}

// dispatch sends an event to all registered handlers
func (bus *DistributedEventBus) dispatch(event Event) error {
	// Snapshot the handler set under the lock: the inner map must not be
	// iterated outside it, or Unsubscribe's delete races the iteration.
	bus.mu.RLock()
	handlers := make([]EventHandler, 0, len(bus.subscribers[event.Topic]))
	for _, handler := range bus.subscribers[event.Topic] {
		handlers = append(handlers, handler)
	}
	bus.mu.RUnlock()

	// Call handlers asynchronously
	for _, handler := range handlers {
		go handler(event)
	}

	return nil
}

// HandleRemoteEvent processes an event received from another node
func (bus *DistributedEventBus) HandleRemoteEvent(eventBytes []byte) error {
	var ce goblinv1.ClusterEvent
	if err := proto.Unmarshal(eventBytes, &ce); err != nil {
		return err
	}

	return bus.dispatch(eventFromWire(&ce))
}

// generateEventID mints a UUIDv7 event/subscription id (operator
// decision 2026-07-28: all ids UUIDv7 where reasonable). Time-ordered
// and collision-free - the previous timestamp scheme collided under
// same-instant calls and silently dropped subscribers.
func generateEventID() string {
	return ident.String(ident.NewV7())
}
