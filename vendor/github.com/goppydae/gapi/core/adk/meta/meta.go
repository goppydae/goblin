// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package meta

// AgentInfo represents all structured metadata discovered from a Python agent file.
type AgentInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Type        string   `json:"type"` // daemon, timer, etc.
	Description string   `json:"description"`
	Interval    int      `json:"interval"`
	Enabled     bool     `json:"enabled"`
	Implements  []string `json:"implements"` // initialize, start, stop, etc.
}
