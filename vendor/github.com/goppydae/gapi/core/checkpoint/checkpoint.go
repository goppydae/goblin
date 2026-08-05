// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package checkpoint dumps and restores process trees with CRIU
// (GOBLIN-DIV-018, research section 4.4/4.5). It is mechanism only: it
// speaks to a caller-supplied local image directory and knows nothing
// about nodes, cluster storage layout, or the {instance_uuid, epoch}
// key those images are stored under. Deciding what to migrate and
// moving images between nodes is policy and lives in the orchestrator.
//
// Linux-only by design, mirroring core/procsig: restore reclaims the
// dumped PIDs with clone3(set_tid) inside a fresh PID namespace, which
// has no meaningful analogue elsewhere. Non-Linux builds compile and
// refuse.
//
// Deliberately no context.Context: CRIU work is a synchronous RPC to a
// criu swrk child, and go-criu offers no cancellation hook. Accepting a
// context we could not honour would be a lie about interruptibility.
// Callers that need a deadline should bound the whole operation.
package checkpoint

import (
	"errors"
	"fmt"
)

// Typed failures. Errors are data: a caller distinguishes "this host
// cannot checkpoint at all" from "this particular dump failed", because
// the first is a scheduling input and the second is a retry decision.
var (
	// ErrUnsupported: checkpoint/restore is Linux-only.
	ErrUnsupported = errors.New("checkpoint: checkpoint/restore is linux-only")
	// ErrNoCriu: the criu binary is not resolvable on PATH.
	ErrNoCriu = errors.New("checkpoint: criu not found on PATH")
	// ErrNotCapable: the process holds neither CAP_CHECKPOINT_RESTORE
	// nor CAP_SYS_ADMIN, so criu cannot dump or restore anything.
	ErrNotCapable = errors.New("checkpoint: missing CAP_CHECKPOINT_RESTORE and CAP_SYS_ADMIN")
	// ErrImagesDir: the image directory is missing or unusable.
	ErrImagesDir = errors.New("checkpoint: image directory unusable")
	// ErrNoRestoredPid: the restore reported success but never told us
	// which PID it produced. Returned rather than guessing, because the
	// caller uses this value to update a locator.
	ErrNoRestoredPid = errors.New("checkpoint: restore reported no pid")
)

// Options tunes a dump or restore. The zero value is the conservative
// case: the dumped process is stopped, and nothing exotic is permitted.
type Options struct {
	// LeaveRunning keeps the source process alive after a successful
	// dump. Migration leaves it FALSE on purpose: a dump that leaves the
	// source running means two live copies of one instance the moment
	// the destination restores, and the image stops being a safe
	// rollback point.
	LeaveRunning bool

	// ShellJob permits dumping a process whose session leader or
	// controlling terminal lies outside the dumped tree.
	ShellJob bool

	// TCPEstablished permits dumping established TCP connections.
	TCPEstablished bool

	// FileLocks permits dumping held file locks.
	FileLocks bool

	// LogLevel and LogFile are passed to criu. LogFile must be a bare
	// filename: criu writes it inside the image directory and rejects
	// paths with separators.
	LogLevel int32
	LogFile  string
}

// Error is a failed checkpoint operation with the context needed to act
// on it. Unwrap exposes the sentinel or the underlying criu failure.
type Error struct {
	Op  string // "dump" or "restore"
	Pid int    // subject pid for dump; 0 for restore
	Dir string // image directory
	Err error
}

func (e *Error) Error() string {
	if e.Pid != 0 {
		return fmt.Sprintf("checkpoint %s (pid %d, dir %s): %v", e.Op, e.Pid, e.Dir, e.Err)
	}
	return fmt.Sprintf("checkpoint %s (dir %s): %v", e.Op, e.Dir, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }
