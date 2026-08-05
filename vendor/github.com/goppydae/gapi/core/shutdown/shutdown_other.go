// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !linux

package shutdown

import "errors"

// Placeholder command values off Linux; the real SysCalls surface is
// Linux-only, so these only ever feed test fakes.
const (
	CmdPowerOff = 0x4321fedc
	CmdRestart  = 0x1234567
	CmdHalt     = 0xcdef0123

	mntDetach = 0x2
)

// ErrUnsupported: init teardown is a Linux obligation.
var ErrUnsupported = errors.New("shutdown: linux-only")

// SysCalls refuses off Linux.
type SysCalls struct{}

func (SysCalls) Sync()                                  {}
func (SysCalls) Unmount(target string, flags int) error { return ErrUnsupported }
func (SysCalls) Reboot(cmd int) error                   { return ErrUnsupported }
