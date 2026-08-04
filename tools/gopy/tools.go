// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build tools

// Package tools anchors the pinned gopy dependency so `go mod tidy` tracks
// the full dependency graph of the gopy command.
package tools

import _ "github.com/go-python/gopy"
