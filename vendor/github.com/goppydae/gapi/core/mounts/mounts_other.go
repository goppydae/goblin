// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !linux

package mounts

import "errors"

// Off Linux the flag values are inert placeholders; MountEarly is only
// ever driven by a real SysMounter on Linux.
const (
	flagNoSuid   = uintptr(1 << 0)
	flagNoDev    = uintptr(1 << 1)
	flagNoExec   = uintptr(1 << 2)
	flagRelatime = uintptr(1 << 3)
)

// ErrUnsupported: early mounts are a Linux boot obligation.
var ErrUnsupported = errors.New("mounts: linux-only")

// SysMounter refuses off Linux.
type SysMounter struct{}

func (SysMounter) Mount(source, target, fstype string, flags uintptr, data string) error {
	return ErrUnsupported
}
func (SysMounter) IsMounted(target string) (bool, error) { return false, ErrUnsupported }
func (SysMounter) MkdirAll(path string) error            { return ErrUnsupported }
