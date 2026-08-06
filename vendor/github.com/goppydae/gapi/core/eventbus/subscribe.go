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
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/metrics"
)

// The bus's subscription surface, split out of eventbus.go when per-
// subscriber ordered delivery pushed that file past the length limit.
// The split is along the interface seam that was already there: this file
// is how a caller JOINS the bus, eventbus.go is how an event MOVES
// through it.

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
// types - e.g. SubscribePrefix("system","",TopicPrefixAgentLifecycle) fires for both
// TopicAgentLifecycleAction (LifecycleControl) and TopicAgentLifecycleStatus
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
	bus.subs[k] = append(bus.subs[k], newSubEntry(id, wrapper))
	return nil
}

// SubscribeCorrelated calls handler at most once, for the first event on the
// exact topic whose ID equals corrID, then unsubscribes. Events with any other
// ID are ignored (not consumed). This is the request/reply correlation
// primitive: a caller publishes a request, then waits for the reply whose ID the
// responder echoed from the request - so concurrent callers sharing one reply
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
	bus.subs[k] = append(bus.subs[k], newSubEntry(id, wrapper))
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
			e.d.stop()
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
			e.d.stop()
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
