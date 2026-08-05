// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !linux

package subreaper

import (
	"context"
	"os"
	"syscall"
)

// BecomeSubreaper is unavailable off Linux (prctl).
func BecomeSubreaper() error { return ErrUnsupported }

// ReapLoop is a no-op off Linux; it returns when ctx is done so
// callers keep a uniform shape.
func ReapLoop(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus)) {
	<-ctx.Done()
}

// ReapLoopWithObserver is a no-op off Linux; observe is never called
// because no wait ever happens.
func ReapLoopWithObserver(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus), observe func(DrainEvent)) {
	<-ctx.Done()
}
