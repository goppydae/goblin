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
	"log/slog"
	"sync"

	"github.com/goppydae/gapi/internal/logattr"
)

// deliveryQueueDepth is how far one subscriber may fall behind before its
// events are dropped.
//
// Bounded rather than unbounded because an unbounded queue in front of a
// stalled handler is a memory leak that presents as latency. Deep enough
// that a burst is absorbed; shallow enough that a handler which has
// genuinely stopped consuming is visible in the log rather than in RSS.
const deliveryQueueDepth = 256

// delivery serializes one subscription's events.
//
// WHY THIS EXISTS: dispatch used to fan out with `go sub.fn(e)`, which
// hands every event its own goroutine and therefore its own race. Two
// events published in order arrived in either order - MEASURED at 13 of
// 20 runs delivering an agent's STOPPED before its RUNNING. Consumers
// that take the FIRST terminal status they see (core/statewatch) then
// report a coin flip, and operator decision 37 makes an ORDERED control
// descriptor the centre of the agent contract. An ordering established
// in the channel and discarded one layer up is not an ordering.
//
// One goroutine per SUBSCRIPTION, not per event: events reach a given
// subscriber in the order they were published, and subscribers stay
// independent of each other - a slow handler delays only itself, which is
// the property the fan-out was buying and the only one worth keeping.
type delivery[T any] struct {
	q     chan Event[T]
	once  sync.Once
	drops uint64
}

// newDelivery starts the subscription's delivery goroutine. It runs until
// stop closes the queue.
func newDelivery[T any](fn Handler[T]) *delivery[T] {
	d := &delivery[T]{q: make(chan Event[T], deliveryQueueDepth)}
	go func() {
		for e := range d.q {
			fn(e)
		}
	}()
	return d
}

// send enqueues e for this subscriber.
//
// NEVER BLOCKS. dispatch holds the bus read lock while calling this, and
// handlers legitimately take the write lock from inside themselves -
// SubscribeOnce removes its own subscription that way - so a blocking
// send here would deadlock the bus against its own consumer.
//
// An overflow is dropped and COUNTED, and the count is reported: a
// dropped event that nothing records is indistinguishable from an event
// that never happened.
//
// The caller holds at least the bus read lock, which is what makes this
// safe against stop's close - see stop.
func (d *delivery[T]) send(e Event[T]) {
	select {
	case d.q <- e:
	default:
		d.drops++
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
			"subscriber queue full, event dropped",
			logattr.EventID(e.ID), logattr.Topic(e.Topic), logattr.Scope(e.Scope),
			slog.Uint64("dropped_total", d.drops))
	}
}

// stop ends the delivery goroutine after it has drained what is queued.
//
// THE CALLER MUST HOLD THE BUS WRITE LOCK. send and stop race on a
// channel close otherwise, and closing a channel a sender is selecting on
// is a panic, not a dropped event. Every call site - the two remove-by-id
// paths, the two Unsubscribe paths and Close - holds it; the sync.Once
// covers repetition, not concurrency.
func (d *delivery[T]) stop() {
	d.once.Do(func() { close(d.q) })
}
