// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package eventbus

import "context"

type Transport[T any] interface {
	PublishRemote(ctx context.Context, e Event[T]) error
	Broadcast(Event[T]) error
	OnRemoteEvent(func(Event[T]))
	Close() error
}
