// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

import (
	"context"

	"github.com/goppydae/gapi/core/eventbus"
)

// Local provides an in-proc "loopback" transport used for testing or single-process mode.
type Local[T any] struct {
	onRemote func(eventbus.Event[T])
}

func (t *Local[T]) PublishRemote(ctx context.Context, e eventbus.Event[T]) error {
	if t.onRemote != nil {
		t.onRemote(e)
	}
	return nil
}

func (t *Local[T]) Broadcast(e eventbus.Event[T]) error {
	return t.PublishRemote(context.Background(), e)
}

func (t *Local[T]) OnRemoteEvent(fn func(eventbus.Event[T])) { t.onRemote = fn }

func (t *Local[T]) Close() error { return nil }
