// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"os"
	"testing"

	gapiproduct "github.com/goppydae/gapi/core/product"
)

// TestMain declares goblin's product identity for this package's tests.
//
// Required, not decorative. These tests boot a real Supervisor in
// process, which reaches phase_local's environment lookups, and the
// embedded kernel has no usable default identity (GAPI-DIV-061,
// GOBLIN-DIV-055) - it panics rather than quietly adopting gapi's
// namespace. goblind declares "goblin" when its root is built; a test
// binary has no root, so it declares the same value here and exercises
// the path a real goblind takes.
func TestMain(m *testing.M) {
	gapiproduct.Set("goblin")
	os.Exit(m.Run())
}
