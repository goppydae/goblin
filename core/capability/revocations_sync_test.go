// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package capability

import (
	"testing"
	"time"
)

// syncPair drives two filters from ONE settable instant, which is what
// anti-entropy needs and atClock cannot give: a shared clock is the
// whole point, since two nodes must agree on which generation a filter
// belongs to.
//
// The start is deliberately aligned to a generation boundary
// (1700000100 = 5666667 * 300). With absolute generations an arbitrary
// instant sits somewhere mid-window, and the cases worth aiming at -
// "an instant before a boundary", "exactly one generation behind" -
// cannot be expressed from an unaligned start.
//
// THE PEER IS CONSTRUCTED HALF A PERIOD EARLIER, ON PURPOSE. Two nodes
// in identical rotation phase cannot demonstrate the renewal loop at
// all: merging a peer's current generation into a window that covers the
// same instants is a no-op. Real nodes start at different times, and the
// offset is introduced the same way they get it - by being built at a
// different moment - so this stays meaningful against any implementation
// that reintroduces process-relative generations.
func syncPair(t *testing.T) (*Revocations, *Revocations, func(time.Duration)) {
	t.Helper()

	aligned := time.Unix(1_700_000_100, 0)
	if aligned.Unix()%int64(TTLMax.Seconds()) != 0 {
		t.Fatalf("test clock %d is not aligned to a %s generation boundary", aligned.Unix(), TTLMax)
	}

	now := aligned.Add(-TTLMax / 2)
	mk := func() *Revocations {
		r := NewRevocationsWithPeriod(TTLMax)
		r.now = func() time.Time { return now }
		r.genIndex = r.indexAt(now)
		r.firstSeen = now
		return r
	}

	b := mk() // half a period into its own window when the test starts
	now = aligned
	a := mk()

	return a, b, func(d time.Duration) { now = now.Add(d) }
}

// exchange runs one full anti-entropy round in both directions, which is
// what the sync loop does per tick.
func exchange(t *testing.T, a, b *Revocations) {
	t.Helper()
	if err := b.Ingest(a.Snapshot()); err != nil {
		t.Fatalf("b.Ingest: %v", err)
	}
	if err := a.Ingest(b.Snapshot()); err != nil {
		t.Fatalf("a.Ingest: %v", err)
	}
}

// TestSync_RepeatedExchangeDoesNotRenewEntries is the defect the whole
// generation-identity design exists to prevent, and the reason
// GOBLIN-DIV-057 could not simply put today's Ingest on a timer.
//
// Two nodes exchanging filters periodically must not renew each other's
// entries. An entry inserted at t=0 has to be forgotten on BOTH nodes by
// t=2*period, exactly as if no sync had ever run. Against an Ingest that
// merges into whichever generation is current locally, the entry is
// renewed on every tick and never ages out - which is the GOBLIN-DIV-015
// saturation reintroduced through the sync path.
func TestSync_RepeatedExchangeDoesNotRenewEntries(t *testing.T) {
	a, b, advance := syncPair(t)
	id := idFor(1)

	a.Revoke(id)

	const tick = 30 * time.Second
	for elapsed := time.Duration(0); elapsed < 2*TTLMax; elapsed += tick {
		exchange(t, a, b)
		advance(tick)
	}
	exchange(t, a, b)

	if a.IsRevoked(id) {
		t.Error("entry survived two full periods on the revoking node: repeated exchange renewed it")
	}
	if b.IsRevoked(id) {
		t.Error("entry survived two full periods on the peer: repeated exchange renewed it")
	}
}

// TestSync_RepairsARevocationFromThePreviousGeneration is the other half
// of the exchange, and the reason a snapshot cannot be the current
// generation alone.
//
// The filter keeps TWO generations because an entry must outlive the
// longest token TTL no matter where in the period it landed. Sync has to
// carry the same two, or it repairs a strictly narrower window than the
// filter maintains: a revocation made late in one window becomes
// invisible to peers the moment the window rolls, while the token it
// revoked is still valid for up to TTLMax.
func TestSync_RepairsARevocationFromThePreviousGeneration(t *testing.T) {
	a, b, advance := syncPair(t)
	id := idFor(2)

	// A revokes late in its window. B is partitioned and never sees the
	// delta broadcast.
	advance(TTLMax - 50*time.Second)
	a.Revoke(id)

	// The window rolls before B reconnects. The entry is now in A's
	// PREVIOUS generation - still live, still covering a token that can
	// remain valid for another 250s.
	advance(100 * time.Second)

	if !a.IsRevoked(id) {
		t.Fatal("precondition: the revoking node forgot its own entry within one period")
	}

	exchange(t, a, b)

	if !b.IsRevoked(id) {
		t.Error("a revocation one generation old was not repaired by sync: B accepts a token A revoked")
	}
}

// TestIngest_DropsAGenerationThisNodeHasRetired is the boundary that
// makes periodic exchange terminate. A filter captured while it was live
// can arrive arbitrarily late - a slow peer, a queued RPC, a node
// rejoining after an hour - and merging it then would resurrect entries
// this node has already, correctly, forgotten.
func TestIngest_DropsAGenerationThisNodeHasRetired(t *testing.T) {
	a, b, advance := syncPair(t)
	id := idFor(3)

	a.Revoke(id)
	stale := a.Snapshot() // captured while the entry is live

	// B moves past every generation that snapshot could belong to. On its
	// own clock B would have retired the entry by now.
	advance(2 * TTLMax)

	if err := b.Ingest(stale); err != nil {
		t.Fatalf("b.Ingest: %v", err)
	}
	if b.IsRevoked(id) {
		t.Error("a retired generation was merged into a live one: the entry came back from the dead")
	}
}
