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
)

// ErrNoPeer reports that a remote publish had nowhere to go: the transport
// is attached and healthy, and no peer is connected to it.
//
// It is part of the Transport CONTRACT rather than any one transport's
// internals, which is why it lives beside the interface. It is also why it
// cannot live in core/transport: that package imports this one.
//
// A single node with no peers is the normal case, not a failure, and the
// bus treats it as such. The sentinel is returned rather than a bare nil
// so that a caller which DOES care - a cluster member that expects peers -
// keeps the distinction a nil would discard.
var ErrNoPeer = errors.New("eventbus: no peer connected")

type Transport[T any] interface {
	PublishRemote(ctx context.Context, e Event[T]) error
	Broadcast(Event[T]) error
	OnRemoteEvent(func(Event[T]))
	Close() error
}
