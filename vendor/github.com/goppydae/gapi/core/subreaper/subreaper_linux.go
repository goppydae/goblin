// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package subreaper

import (
	"context"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// BecomeSubreaper marks this process as a child subreaper: orphaned
// descendants reparent to it rather than to pid 1. Called before any
// other initialization (Phase 0); when the process IS pid 1 the call
// is a harmless no-op for correctness but kept for uniformity.
func BecomeSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// ReapLoop drains terminated children for the supervisor's lifetime.
// It triggers on SIGCHLD (caller supplies the subscribed channel), on
// a safety tick (coalesced or pre-subscription signal edges must not
// strand zombies), and once at startup. Every reaped pid is forwarded
// to notify with its true wait status; the agent manager decides
// whether it was a known agent or an adopted orphan.
func ReapLoop(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus)) {
	ReapLoopWithObserver(ctx, sigchld, notify, nil)
}

// ReapLoopWithObserver is ReapLoop with a diagnostic seam: observe, when
// non-nil, is called synchronously with every Wait4 outcome, including
// the ones the loop itself discards.
//
// It exists for GAPI-DIV-043, whose exit requires the Wait4 errno to be
// read out of a CI log rather than reproduced locally. observe is called
// on the loop's goroutine and must not block; a nil observer leaves the
// loop's control flow identical to what it was before the seam existed,
// which is deliberate - instrumentation that perturbs the timing of an
// intermittent failure can spend the failure without explaining it.
func ReapLoopWithObserver(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus), observe func(DrainEvent)) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	drain := func(trigger DrainTrigger) {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if observe != nil {
				observe(DrainEvent{Trigger: trigger, Pid: pid, Status: ws, Err: err})
			}
			if err != nil || pid <= 0 {
				// ECHILD (no children) or no zombies right now.
				return
			}
			notify(pid, ws)
		}
	}

	drain(DrainStartup)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigchld:
			drain(DrainSigchld)
		case <-ticker.C:
			drain(DrainTick)
		}
	}
}
