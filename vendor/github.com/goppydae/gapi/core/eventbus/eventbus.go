package eventbus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/uuid"
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
	Broadcast bool
	Tags      []string
}

func NewEvent[T any](scope, namespace, topic, source string, payload T, broadcast bool, tags ...string) Event[T] {
	return Event[T]{
		ID:        uuid.New().String(),
		Scope:     scope,
		Namespace: namespace,
		Topic:     topic,
		Source:    source,
		Payload:   payload,
		Broadcast: broadcast,
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
		t.OnRemoteEvent(func(e Event[T]) { _ = b.dispatch(e) })
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
	m[k] = append(m[k], subEntry[T]{id: id, fn: fn})
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
			break
		}
	}
	if len(b.prefixSubs[k]) == 0 {
		delete(b.prefixSubs, k)
	}
}

// ---- Subscription APIs ----

func (b *EventBus[T]) Subscribe(scope, namespace, topic string, fn Handler[T]) error {
	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := fullKey(scope, namespace, topic)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("eventbus closed")
	}
	b.ensureInitLocked()
	b.addSubLocked(b.subs, k, fn)

	// Update subscriber count metric
	metrics.UpdateSubscriberCount(topic, len(b.subs[k]))

	return nil
}

// SubscribePrefix subscribes to every topic that begins with topicPrefix.
//
// TYPE HAZARD: a prefix matches sibling topics that may carry different payload
// types — e.g. SubscribePrefix("system","","agent/lifecycle") fires for both
// "agent/lifecycle.action" (LifecycleControl) and "agent/lifecycle.status"
// (LifecycleStatus). A handler that assumes one proto type will panic on
// UnmarshalTo/type-assert when the other arrives. Prefer an exact Subscribe when
// the handler is payload-typed, or use SubscribePrefixTyped to filter by type.
func (b *EventBus[T]) SubscribePrefix(scope, namespace, topicPrefix string, fn Handler[T]) error {
	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := "__MATCH:" + fullKey(scope, namespace, topicPrefix)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("eventbus closed")
	}
	b.ensureInitLocked()
	b.addSubLocked(b.prefixSubs, k, fn)
	return nil
}

// SubscribeOnce calls handler at most once for the exact topic, then unsubscribes.
//
// Deprecated: uncorrelated one-shot subscriptions let concurrent callers
// steal each other's events (review R15). Use SubscribeCorrelated for
// request/reply, or WaitForTopic for bounded phase gates. Scheduled for
// removal; see deprecation.jsonl.
func (bus *EventBus[T]) SubscribeOnce(scope, namespace, topic string, handler Handler[T]) error {
	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := fullKey(scope, namespace, topic)

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return fmt.Errorf("eventbus closed")
	}
	bus.ensureInitLocked()

	var once sync.Once
	bus.nextSubID++
	id := bus.nextSubID
	wrapper := func(e Event[T]) {
		once.Do(func() {
			handler(e)
			bus.removeSubByID(k, id)
		})
	}
	bus.subs[k] = append(bus.subs[k], subEntry[T]{id: id, fn: wrapper})
	return nil
}

// SubscribeCorrelated calls handler at most once, for the first event on the
// exact topic whose ID equals corrID, then unsubscribes. Events with any other
// ID are ignored (not consumed). This is the request/reply correlation
// primitive: a caller publishes a request, then waits for the reply whose ID the
// responder echoed from the request — so concurrent callers sharing one reply
// topic don't steal each other's responses. The Envelope carries the ID over the
// wire, so this works across transports.
func (bus *EventBus[T]) SubscribeCorrelated(scope, namespace, topic, corrID string, handler Handler[T]) error {
	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := fullKey(scope, namespace, topic)

	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return fmt.Errorf("eventbus closed")
	}
	bus.ensureInitLocked()

	// Self-removal is by unique id: concurrent callers subscribing from the
	// same code site are distinct closure instances with one shared code
	// pointer, so pointer-identity removal would strip another caller's
	// live subscription (lost replies).
	var once sync.Once
	bus.nextSubID++
	id := bus.nextSubID
	wrapper := func(e Event[T]) {
		if e.ID != corrID {
			return
		}
		once.Do(func() {
			handler(e)
			bus.removeSubByID(k, id)
		})
	}
	bus.subs[k] = append(bus.subs[k], subEntry[T]{id: id, fn: wrapper})
	return nil
}

// Unsubscribe removes a single handler from an exact topic.
//
// LIMITATION: func values are only comparable by code pointer, which every
// closure instance from one code site shares - so this can only distinguish
// handlers defined at different code sites. Do not use it to remove one of
// several same-callsite subscriptions; the one-shot APIs (SubscribeOnce,
// SubscribeCorrelated, WaitForTopic, SubscribePrefixWithContext) remove
// themselves by unique id instead.
func (bus *EventBus[T]) Unsubscribe(scope, namespace, topic string, target Handler[T]) {
	k := fullKey(scope, namespace, topic)

	bus.mu.Lock()
	defer bus.mu.Unlock()
	entries := bus.subs[k]
	for i, e := range entries {
		if fmt.Sprintf("%p", e.fn) == fmt.Sprintf("%p", target) {
			bus.subs[k] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(bus.subs[k]) == 0 {
		delete(bus.subs, k)
	}
}

// UnsubscribePrefix removes a single handler from a prefix subscription.
// Shares Unsubscribe's code-pointer limitation.
func (bus *EventBus[T]) UnsubscribePrefix(scope, namespace, topicPrefix string, target Handler[T]) {
	k := "__MATCH:" + fullKey(scope, namespace, topicPrefix)

	bus.mu.Lock()
	defer bus.mu.Unlock()
	entries := bus.prefixSubs[k]
	for i, e := range entries {
		if fmt.Sprintf("%p", e.fn) == fmt.Sprintf("%p", target) {
			bus.prefixSubs[k] = append(entries[:i], entries[i+1:]...)
			break
		}
	}
	if len(bus.prefixSubs[k]) == 0 {
		delete(bus.prefixSubs, k)
	}
}

// SubscribePrefixWithContext subscribes with automatic cleanup on context cancellation.
// This prevents subscription leaks in long-running operations by removing the subscription
// when the context is cancelled or times out.
func (bus *EventBus[T]) SubscribePrefixWithContext(ctx context.Context, scope, namespace, topicPrefix string, handler Handler[T]) error {
	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := "__MATCH:" + fullKey(scope, namespace, topicPrefix)

	bus.mu.Lock()
	if bus.closed {
		bus.mu.Unlock()
		return fmt.Errorf("eventbus closed")
	}
	bus.ensureInitLocked()
	// Cleanup removes by unique id: concurrent same-callsite subscribers
	// (e.g. statewatch waiting on several agents) must not remove each
	// other's live subscriptions on context cancellation.
	id := bus.addSubLocked(bus.prefixSubs, k, handler)
	bus.mu.Unlock()

	go func() {
		<-ctx.Done()
		bus.removePrefixSubByID(k, id)
	}()

	return nil
}

// SubscribePrefixTyped subscribes to a topic prefix but invokes handler only for
// events whose *anypb.Any payload unmarshals into type M. It is the type-safe
// companion to SubscribePrefix: events carrying a different proto type (a sibling
// topic under the same prefix), or a nil/undecodable payload, are silently
// skipped instead of crashing a type-specific handler.
func SubscribePrefixTyped[M proto.Message](
	bus *EventBus[*anypb.Any],
	scope, namespace, topicPrefix string,
	handler func(e Event[*anypb.Any], msg M),
) error {
	return bus.SubscribePrefix(scope, namespace, topicPrefix, func(e Event[*anypb.Any]) {
		if e.Payload == nil {
			return
		}
		decoded, err := e.Payload.UnmarshalNew()
		if err != nil {
			return
		}
		typed, ok := decoded.(M)
		if !ok {
			return
		}
		handler(e, typed)
	})
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
		var terr error
		if e.Broadcast {
			terr = b.transport.Broadcast(e)
		} else {
			terr = b.transport.PublishRemote(context.Background(), e)
		}
		if terr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "transport publish failed", logattr.Err(terr), logattr.EventID(e.ID), logattr.Topic(e.Topic))
		}
	}

	// Record event publish metric
	metrics.RecordEvent(e.Topic)

	slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "publishing event", logattr.Topic(e.Topic), logattr.EventID(e.ID), logattr.Scope(e.Scope))

	return b.dispatch(e)
}

func (b *EventBus[T]) dispatch(e Event[T]) error {
	k := fullKey(e.Scope, e.Namespace, e.Topic)

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Exact match
	if handlers, ok := b.subs[k]; ok {
		for _, sub := range handlers {
			go sub.fn(e)
		}
	}
	// Prefix match
	for key, handlers := range b.prefixSubs {
		prefix := strings.TrimPrefix(key, "__MATCH:")
		if strings.HasPrefix(k, prefix) {
			for _, sub := range handlers {
				go sub.fn(e)
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
func (t *LocalTransport[T]) Broadcast(Event[T]) error                            { return nil }
func (t *LocalTransport[T]) OnRemoteEvent(func(Event[T]))                        {}
func (t *LocalTransport[T]) Close() error                                        { return nil }
