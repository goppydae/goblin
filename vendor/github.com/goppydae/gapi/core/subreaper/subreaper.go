// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package subreaper implements the PID-1 orphan-reaping obligation
// (GAPI-DIV-027): the supervisor registers as a child subreaper so
// orphaned descendants reparent to it instead of pid 1, and a reap
// loop collects their exit statuses - zombie accumulation is a kernel
// obligation, not an optimization. Linux-only by nature; non-Linux
// builds compile and refuse.
package subreaper

import (
	"errors"
	"syscall"
)

// ErrUnsupported: subreaper semantics are a Linux prctl feature.
var ErrUnsupported = errors.New("subreaper: linux-only")

// DrainTrigger names what woke a drain pass. It exists because the
// wake source is diagnostic: a zombie collected only ever by DrainTick
// means the SIGCHLD edge for it was lost, which is a different defect
// from one where the signal arrived and the wait still came up empty.
type DrainTrigger int

const (
	// DrainStartup is the unconditional drain ReapLoop performs before
	// entering its select, so a zombie that died pre-subscription is
	// still collected.
	DrainStartup DrainTrigger = iota
	// DrainSigchld is a drain woken by a delivered SIGCHLD.
	DrainSigchld
	// DrainTick is a drain woken by the safety ticker, which covers
	// coalesced and pre-subscription signal edges.
	DrainTick
)

// String renders the trigger for diagnostic output.
func (t DrainTrigger) String() string {
	switch t {
	case DrainStartup:
		return "startup"
	case DrainSigchld:
		return "sigchld"
	case DrainTick:
		return "tick"
	default:
		return "unknown"
	}
}

// DrainEvent is one Wait4 outcome inside the reap loop, reported to an
// observer supplied by ReapLoopWithObserver.
//
// The loop's normal operation discards every one of these: a wait that
// returns no zombie is indistinguishable from one that fails, because
// both simply end the drain. That is fine for reaping and useless for
// diagnosis, which is why the observer exists (GAPI-DIV-043).
type DrainEvent struct {
	// Trigger is what woke this drain pass.
	Trigger DrainTrigger
	// Pid is Wait4's return: a positive reaped pid, 0 when a child
	// exists but none is ready, or -1 on error.
	Pid int
	// Status is meaningful only when Pid is positive.
	Status syscall.WaitStatus
	// Err is the Wait4 errno, nil on success. ECHILD - no children at
	// all - is the routine case and not a fault.
	Err error
}
