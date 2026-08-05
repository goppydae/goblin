// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !linux

package agentmgr

import "time"

// BootTime returns the instant the system booted.
//
// There is no portable way to ask for it, and gapi's supervision paths
// are Linux-only in practice, so non-Linux builds return the current
// time. OnBootSec therefore behaves as OnStartupSec there - late rather
// than wrong, and the two differ only on a host that has been up longer
// than the declared duration.
func BootTime() time.Time {
	return time.Now()
}
