// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package scheduler

import (
	"github.com/goppydae/goblin/internal/hlc"
)

// Locator is the mutable runtime location of an instance: which node,
// which process incarnation. It lives in gossip (heartbeats), never in
// Raft - the three-layer identity split (DDR-3/4/5). Updates resolve
// last-writer-wins on the HLC timestamp.
type Locator struct {
	NodeID     string
	Pid        int
	StartEpoch uint64
	PidNsInode uint64
	At         hlc.Timestamp
}

// ObserveLocator records an instance's runtime locator. Stale updates
// (HLC not after the recorded one) are dropped: last writer wins.
func (s *Scheduler) ObserveLocator(instanceID string, loc Locator) {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if s.locators == nil {
		s.locators = make(map[string]Locator)
	}
	if cur, ok := s.locators[instanceID]; ok && !loc.At.After(cur.At) {
		return
	}
	s.locators[instanceID] = loc
}

// LookupLocator returns the last-known runtime locator of an instance.
func (s *Scheduler) LookupLocator(instanceID string) (Locator, bool) {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	loc, ok := s.locators[instanceID]
	return loc, ok
}
