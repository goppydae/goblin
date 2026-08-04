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

	"github.com/goppydae/goblin/internal/hlc"
)

func TestObserveLocator_LastWriterWins(t *testing.T) {
	s := NewScheduler(NewMockStore(), nil, nil, nil, nil)

	newer := Locator{NodeID: "node-2", Pid: 42, StartEpoch: 900, PidNsInode: 7,
		At: hlc.Timestamp{Wall: 2000, Node: "node-2"}}
	older := Locator{NodeID: "node-1", Pid: 41, StartEpoch: 800, PidNsInode: 7,
		At: hlc.Timestamp{Wall: 1000, Node: "node-1"}}

	s.ObserveLocator("inst-1", newer)
	s.ObserveLocator("inst-1", older) // stale: must not overwrite

	got, ok := s.LookupLocator("inst-1")
	if !ok {
		t.Fatal("locator not recorded")
	}
	if got.NodeID != "node-2" || got.Pid != 42 || got.StartEpoch != 900 {
		t.Fatalf("stale locator overwrote newer one: %+v", got)
	}

	// A genuinely newer update replaces.
	newest := Locator{NodeID: "node-3", Pid: 43, StartEpoch: 950, PidNsInode: 7,
		At: hlc.Timestamp{Wall: 3000, Node: "node-3"}}
	s.ObserveLocator("inst-1", newest)
	if got, _ := s.LookupLocator("inst-1"); got.NodeID != "node-3" {
		t.Fatalf("newer locator did not replace: %+v", got)
	}
}

func TestForgetHeartbeat_DropsLocator(t *testing.T) {
	s := NewScheduler(NewMockStore(), nil, nil, nil, nil)
	s.ObserveLocator("inst-1", Locator{NodeID: "node-1", At: hlc.Timestamp{Wall: 1, Node: "node-1"}})
	s.forgetHeartbeat("inst-1")
	if _, ok := s.LookupLocator("inst-1"); ok {
		t.Fatal("locator survived forgetHeartbeat: a replaced instance id must not shadow future observations")
	}
}
