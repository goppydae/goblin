// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package procsig delivers signals to agent processes guarded by their
// start epoch (DDR-5, GAPI-DIV-016): a signal aimed at a dead process
// whose PID was recycled must never hit the new occupant. Linux-only by
// design (operator decision 2026-07-28) - delivery uses pidfd_open +
// pidfd_send_signal with no fallback; non-Linux builds compile but
// refuse to deliver.
package procsig

import "errors"

// Typed delivery failures. Errors are data; there is deliberately no
// retry on ErrStaleEpoch - a stale epoch means the target is gone and
// the orchestrator must re-resolve, not hammer a recycled PID.
var (
	// ErrStaleEpoch: the process at this PID is not the one the caller
	// meant (start epoch mismatch - the PID was recycled).
	ErrStaleEpoch = errors.New("procsig: stale start epoch, refusing delivery")
	// ErrProcessGone: no process holds this PID.
	ErrProcessGone = errors.New("procsig: process gone")
	// ErrUnsupported: signal delivery is Linux-only.
	ErrUnsupported = errors.New("procsig: signal delivery is linux-only")
	// ErrPidfdUnsupported: the kernel does not offer pidfd_open, so
	// neither this package nor the os/exec handle path can pin a
	// process. Signal delivery would silently degrade to kill-by-PID,
	// which is the PID-recycling hazard itself (GAPI-DIV-016).
	ErrPidfdUnsupported = errors.New("procsig: kernel does not support pidfd_open")
)

// ProcessIdentity is the mutable runtime locator of a process: the PID
// plus the two fields that disambiguate a PID across recycling and
// namespaces (DDR-3/4/5). It travels in gossip, never in Raft.
type ProcessIdentity struct {
	Pid        int
	StartEpoch uint64
	PidNsInode uint64
}
