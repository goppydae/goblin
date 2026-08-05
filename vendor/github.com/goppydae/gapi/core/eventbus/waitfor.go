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
	"sync"
	"time"

	"github.com/goppydae/gapi/core/clock"
)

// ErrWaitTimeout is returned when WaitForTopic's bound expires before the
// topic fires. Callers at a boot phase gate treat it as a loud failure:
// "hang forever" is not an acceptable PID 1 outcome (review R13).
var ErrWaitTimeout = errors.New("eventbus: wait for topic timed out")

// WaitForTopic blocks until an event arrives on the exact topic, the context
// is cancelled, or the timeout expires - whichever comes first. It is the
// bounded phase-gate primitive: unlike a bare subscription, it can never
// wait forever. The clock is injectable for deterministic tests; nil uses
// the real clock.
func (bus *EventBus[T]) WaitForTopic(ctx context.Context, scope, namespace, topic string, timeout time.Duration, clk clock.Clock) error {
	if clk == nil {
		clk = clock.RealClock{}
	}

	if !validScopes[scope] {
		return fmt.Errorf("invalid scope: %s", scope)
	}
	k := fullKey(scope, namespace, topic)

	fired := make(chan struct{})
	var once sync.Once
	wrapper := Handler[T](func(Event[T]) {
		once.Do(func() { close(fired) })
	})

	// Register by unique id so concurrent same-callsite waiters cannot
	// remove each other's subscriptions on cleanup.
	bus.mu.Lock()
	if bus.closed {
		bus.mu.Unlock()
		return fmt.Errorf("eventbus closed")
	}
	bus.ensureInitLocked()
	id := bus.addSubLocked(bus.subs, k, wrapper)
	bus.mu.Unlock()
	defer bus.removeSubByID(k, id)

	select {
	case <-fired:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-clk.After(timeout):
		return fmt.Errorf("%w: topic %s/%s/%s did not fire within %s", ErrWaitTimeout, scope, namespace, topic, timeout)
	}
}
