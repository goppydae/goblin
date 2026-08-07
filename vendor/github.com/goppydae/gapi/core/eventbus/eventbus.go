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
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/goppydae/gapi/internal/ident"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/metrics"
	"github.com/goppydae/gapi/internal/logattr"
)

// ---- Event Definition ----

type Event[T any] struct {
	ID        string
	Scope     string
	Namespace string
	Topic     string
	Payload   T
	Source    string
	Tags      []string
}

// NewEvent no longer takes a broadcast flag (GAPI-DIV-106). It selected
// between two Transport methods that did the same thing, because the
// QUIC transport addressed a single peer: "broadcast" and "publish to
// the one peer you have" are indistinguishable. Now that the transport
// holds a peer SET, a remote publish IS to every peer, so there is no
// second behaviour left for a flag to choose - and the flag was never
// carried on the wire, so no receiver could ever act on it.
func NewEvent[T any](scope, namespace, topic, source string, payload T, tags ...string) Event[T] {
	return Event[T]{
		ID:        ident.NewV7String(),
		Scope:     scope,
		Namespace: namespace,
		Topic:     topic,
		Source:    source,
		Payload:   payload,
		Tags:      tags,
	}
}

type Handler[T any] func(Event[T])

// ---- EventBus Implementation ----

// subEntry pairs a handler with a bus-unique id. Handlers created at one
// code site are distinct closure instances sharing one code pointer, so the
// id - not any function-pointer comparison - is subscription identity.
type subEntry[T any] struct {
	id uint64
	fn Handler[T]
	// d serializes this subscription's deliveries. See delivery.go: it is
	// what makes the order events are published the order they arrive.
	d *delivery[T]
}

// newSubEntry builds an entry and starts its delivery goroutine. Every
// construction goes through here, so no subscription can exist without
// one.
func newSubEntry[T any](id uint64, fn Handler[T]) subEntry[T] {
	return subEntry[T]{id: id, fn: fn, d: newDelivery(fn)}
}

type EventBus[T any] struct {
	mu         sync.RWMutex
	subs       map[string][]subEntry[T]
	prefixSubs map[string][]subEntry[T]
	nextSubID  uint64
	transport  Transport[T]
	closed     bool
}

type Options struct{}

func NewEventBus[T any](t Transport[T], _ ...Options) *EventBus[T] {
	b := &EventBus[T]{
		subs:       make(map[string][]subEntry[T]),
		prefixSubs: make(map[string][]subEntry[T]),
		transport:  t,
	}
	if t != nil {
		// VALIDATE AT THE INGRESS (GAPI-DIV-100). This is the only path
		// into the bus carrying bytes from another process, and it was
		// the only one that skipped ValidateEvent: Publish refuses an
		// invalid scope, while a remote event went straight to dispatch
		// and simply missed every key. An unroutable event produced no
		// error to the publisher and no line in the log, so a real
		// defect presented as a silence and cost a day to trace.
		//
		// There is nobody to return an error to here, so the log IS the
		// boundary failure.
		t.OnRemoteEvent(func(e Event[T]) {
			if err := ValidateEvent(e); err != nil {
				slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
					"rejected invalid remote event", logattr.Event("reject"),
					logattr.EventID(e.ID), logattr.Topic(e.Topic),
					logattr.Scope(e.Scope), logattr.Source(e.Source), logattr.Err(err))
				return
			}
			_ = b.dispatch(e)
		})
	}
	return b
}

func NewInprocBus[T any]() *EventBus[T] {
	return NewEventBus[T](nil)
}

func (b *EventBus[T]) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.transport != nil {
		_ = b.transport.Close()
	}
	// Every subscription owns a goroutine, so dropping the maps without
	// stopping them would leak one per subscriber for the process's life.
	for _, m := range []map[string][]subEntry[T]{b.subs, b.prefixSubs} {
		for _, entries := range m {
			for _, e := range entries {
				e.d.stop()
			}
		}
	}
	b.subs = nil
	b.prefixSubs = nil
	return nil
}

var validScopes = map[string]bool{"system": true, "user": true, "admin": true}

func ValidateEvent[T any](e Event[T]) error {
	if !validScopes[e.Scope] {
		return fmt.Errorf("invalid scope: %s", e.Scope)
	}
	if strings.TrimSpace(e.Topic) == "" {
		return fmt.Errorf("topic cannot be empty")
	}
	// Namespace can be empty (global/default)
	return nil
}

func fullKey(scope, namespace, topic string) string {
	if namespace == "" {
		namespace = "default"
	}
	return scope + "/" + namespace + "/" + topic
}

func (b *EventBus[T]) ensureInitLocked() {
	if b.subs == nil {
		b.subs = make(map[string][]subEntry[T])
	}
	if b.prefixSubs == nil {
		b.prefixSubs = make(map[string][]subEntry[T])
	}
}

// addSubLocked appends fn under key k in m and returns its unique id.
func (b *EventBus[T]) addSubLocked(m map[string][]subEntry[T], k string, fn Handler[T]) uint64 {
	b.nextSubID++
	id := b.nextSubID
	m[k] = append(m[k], newSubEntry(id, fn))
	return id
}

// removeSubByID removes the entry with the given id from the exact-topic
// subscriptions. Safe to call from handler goroutines.
func (b *EventBus[T]) removeSubByID(k string, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := b.subs[k]
	for i, e := range entries {
		if e.id == id {
			b.subs[k] = append(entries[:i], entries[i+1:]...)
			e.d.stop()
			break
		}
	}
	if len(b.subs[k]) == 0 {
		delete(b.subs, k)
	}
}

// removePrefixSubByID is removeSubByID for prefix subscriptions.
func (b *EventBus[T]) removePrefixSubByID(k string, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := b.prefixSubs[k]
	for i, e := range entries {
		if e.id == id {
			b.prefixSubs[k] = append(entries[:i], entries[i+1:]...)
			e.d.stop()
			break
		}
	}
	if len(b.prefixSubs[k]) == 0 {
		delete(b.prefixSubs, k)
	}
}

// ---- Publish / Dispatch ----

func (b *EventBus[T]) Publish(e Event[T]) error {
	if err := ValidateEvent(e); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "rejected invalid event", logattr.Event("reject"), logattr.EventID(e.ID), logattr.Topic(e.Topic), logattr.Scope(e.Scope))
		return err
	}

	if b == nil {
		return fmt.Errorf("eventbus: publish on nil bus")
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "structured event",
		logattr.Module("eventbus"), logattr.Event("publish"), logattr.EventID(e.ID),
		logattr.Source(e.Source), logattr.Topic(e.Topic),
		logattr.PayloadType(fmt.Sprintf("%T", e.Payload)))

	if b.transport != nil {
		// ONE ARM, because there was only ever one operation
		// (GAPI-DIV-106). Broadcast delegated to PublishRemote, so the
		// branch chose between a method and itself.
		terr := b.transport.PublishRemote(context.Background(), e)
		// Having no peer is not a publish failure (GAPI-DIV-095). Only
		// this ONE sentinel is demoted, and only by matching it: lowering
		// the level for every transport error would hide a real one
		// behind the same treatment, which is the inversion this fix
		// exists to avoid.
		switch {
		case errors.Is(terr, ErrNoPeer):
			slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "no peer for remote publish", logattr.EventID(e.ID), logattr.Topic(e.Topic))
		case terr != nil:
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "transport publish failed", logattr.Err(terr), logattr.EventID(e.ID), logattr.Topic(e.Topic))
		}
	}

	// Record event publish metric
	metrics.RecordEvent(e.Topic)

	slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "publishing event", logattr.Topic(e.Topic), logattr.EventID(e.ID), logattr.Scope(e.Scope))

	return b.dispatch(e)
}

// dispatch hands the event to every matching subscription's delivery
// queue.
//
// ENQUEUE, DO NOT SPAWN. `go sub.fn(e)` gave each event its own goroutine
// and so destroyed the publisher's ordering before any subscriber saw it
// (delivery.go records the measurement). Handing the event to the
// subscriber's queue keeps subscribers concurrent with each other while
// each one sees the sequence that was actually published.
//
// The read lock is held across the sends, which is why they must not
// block: see delivery.send.
func (b *EventBus[T]) dispatch(e Event[T]) error {
	k := fullKey(e.Scope, e.Namespace, e.Topic)

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Exact match
	if handlers, ok := b.subs[k]; ok {
		for _, sub := range handlers {
			sub.d.send(e)
		}
	}
	// Prefix match
	for key, handlers := range b.prefixSubs {
		prefix := strings.TrimPrefix(key, "__MATCH:")
		if strings.HasPrefix(k, prefix) {
			for _, sub := range handlers {
				sub.d.send(e)
			}
		}
	}
	return nil
}

// ---- Protobuf Helpers ----

// UnmarshalAnyPayload extracts and unmarshals a protobuf Any payload from an event.
// Call it as:  eventbus.UnmarshalAnyPayload(e, &myProto)
func UnmarshalAnyPayload(e Event[*anypb.Any], target proto.Message) error {
	if e.Payload == nil {
		return fmt.Errorf("no payload in event")
	}
	return e.Payload.UnmarshalTo(target)
}

// LocalTransport is a no-op transport for local-only operation.
type LocalTransport[T any] struct{}

func (t *LocalTransport[T]) PublishRemote(ctx context.Context, e Event[T]) error { return nil }
func (t *LocalTransport[T]) OnRemoteEvent(func(Event[T]))                        {}
func (t *LocalTransport[T]) Close() error                                        { return nil }
