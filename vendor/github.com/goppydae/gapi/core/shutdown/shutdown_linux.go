// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build linux

package shutdown

import "golang.org/x/sys/unix"

// reboot(2) commands, aliased so tests assert the mapping portably.
const (
	CmdPowerOff = unix.LINUX_REBOOT_CMD_POWER_OFF
	CmdRestart  = unix.LINUX_REBOOT_CMD_RESTART
	CmdHalt     = unix.LINUX_REBOOT_CMD_HALT

	mntDetach = unix.MNT_DETACH
)

// SysCalls is the real privileged surface.
type SysCalls struct{}

func (SysCalls) Sync() { unix.Sync() }
func (SysCalls) Unmount(target string, flags int) error {
	return unix.Unmount(target, flags)
}
func (SysCalls) Reboot(cmd int) error { return unix.Reboot(cmd) }
