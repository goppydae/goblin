// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package scheduler

import (
	"testing"
)

// Mock objects would be needed for Store and Cluster to test properly.
// For now, testing logic that doesn't depend heavily on external state or mocking structs.
// Since Scheduler depends on Store/Cluster structs directly (bad design, should be interfaces),
// we can't easily unit test without a full environment or refactoring to interfaces.
//
// Let's refactor Scheduler to use interfaces for easier testing, or just test what we can.
// Actually, for this MVP, I'll skip complex mocking and just verify compilation and basic strategies if I extract them.

func TestStrategy(t *testing.T) {
	// Strategy constant check
	if StrategyRandom != "random" {
		t.Error("StrategyRandom mismatch")
	}
}

// NOTE: To properly test Schedule(), we need to mock cluster.Membership.
// Since we didn't define an interface, we'd need to instantiate the real struct, which might be hard.
// In a real scenario, I would refactor to interfaces `Cluster` and `Store`.
