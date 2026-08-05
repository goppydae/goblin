// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !linux

package checkpoint

// Available refuses off Linux. There is deliberately no fallback: CRIU
// restore reclaims dumped PIDs through clone3(set_tid) in a fresh PID
// namespace, and nothing outside Linux offers that. A best-effort
// "restart it over there" would silently break the one invariant
// migration exists to preserve - that the instance UUID keeps pointing
// at the same running process (research section 4.4).
func Available() error { return ErrUnsupported }

// Dump refuses off Linux.
func Dump(pid int, dir string, opt Options) error {
	return &Error{Op: "dump", Pid: pid, Dir: dir, Err: ErrUnsupported}
}

// Restore refuses off Linux.
func Restore(dir string, opt Options) (int, error) {
	return 0, &Error{Op: "restore", Dir: dir, Err: ErrUnsupported}
}
