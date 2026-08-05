// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package capability

import (
	"encoding/binary"
	"testing"
	"time"
)

// atClock drives a filter from a settable instant so rotation can be
// tested without sleeping. Rotation periods are 300s; a test that waited
// would not be a test anyone runs.
//
// The instant is ALIGNED to a generation boundary (1700000100 =
// 5666667 * 300). Generations are absolute windows now, so an arbitrary
// instant lands somewhere mid-window and "advance to just before a
// boundary" stops meaning what it says - the worst case the correctness
// floor below aims at could not be expressed from an unaligned start.
func atClock(t *testing.T, period time.Duration) (*Revocations, func(time.Duration)) {
	t.Helper()

	now := time.Unix(1_700_000_100, 0)
	r := NewRevocationsWithPeriod(period)
	if now.Unix()%int64(r.period/time.Second) != 0 {
		t.Fatalf("test clock %d is not aligned to a %s generation boundary", now.Unix(), r.period)
	}
	r.now = func() time.Time { return now }
	r.genIndex = r.indexAt(now)
	r.firstSeen = now
	return r, func(d time.Duration) { now = now.Add(d) }
}

func idFor(n uint64) []byte {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b[8:], n)
	return b
}

// TestRotation_EntrySurvivesAtLeastOneFullPeriod is the correctness
// floor. Forgetting a revocation while the token it revokes is still
// valid is a security hole, not a tuning choice, so an entry must
// outlive the longest token TTL no matter when in the period it landed.
func TestRotation_EntrySurvivesAtLeastOneFullPeriod(t *testing.T) {
	r, advance := atClock(t, TTLMax)
	id := idFor(1)

	// Worst case: recorded an instant before a rotation boundary.
	advance(TTLMax - time.Millisecond)
	r.Revoke(id)

	advance(TTLMax)
	if !r.IsRevoked(id) {
		t.Fatal("entry dropped before one full period elapsed; a live token would be accepted")
	}
}

// TestRotation_EntryIsEventuallyForgotten is the other half. Without it
// the filter saturates: at 2000 entries it refuses 15% of VALID tokens,
// at 4000 more than half.
func TestRotation_EntryIsEventuallyForgotten(t *testing.T) {
	r, advance := atClock(t, TTLMax)
	id := idFor(2)
	r.Revoke(id)

	if !r.IsRevoked(id) {
		t.Fatal("entry not recorded")
	}

	// Past every live generation.
	advance(TTLMax * generations)
	advance(time.Second)

	if r.IsRevoked(id) {
		t.Error("entry survived every generation; the filter never forgets and will saturate")
	}
}

// TestRotation_HappensOnTheReadPath too. A node that revokes nothing
// still has to retire generations, or a quiet node keeps refusing tokens
// revoked hours ago - and only reads would reveal it.
func TestRotation_HappensOnTheReadPath(t *testing.T) {
	r, advance := atClock(t, TTLMax)
	id := idFor(3)
	r.Revoke(id)

	advance(TTLMax * (generations + 1))

	// No Revoke in between: if rotation only ran on writes, this stays
	// true forever.
	if r.IsRevoked(id) {
		t.Error("a filter with no writes never rotated")
	}
}

// TestRotation_LongIdleGapCatchesUpInOneCall guards the loop in
// rotateLocked. Shifting a single generation per call would leave a node
// idle for hours carrying stale bits until it happened to be called
// enough times.
func TestRotation_LongIdleGapCatchesUpInOneCall(t *testing.T) {
	r, advance := atClock(t, TTLMax)
	r.Revoke(idFor(4))

	advance(TTLMax * 50)

	if r.IsRevoked(idFor(4)) {
		t.Error("a long idle gap did not fully retire old generations")
	}
	if got := r.Stats().CurrentGeneration; got != 0 {
		t.Errorf("current generation carries %d entries after a long gap, want 0", got)
	}
}

// TestNewRevocations_RefusesAPeriodBelowTokenLifetime pins the floor as
// code rather than documentation. A caller asking to forget revocations
// faster than tokens expire is asking for a hole.
func TestNewRevocations_RefusesAPeriodBelowTokenLifetime(t *testing.T) {
	r := NewRevocationsWithPeriod(time.Second)
	if got := r.Stats().Period; got != TTLMax {
		t.Errorf("period %s was accepted below TTLMax %s", got, TTLMax)
	}
}

// TestStats_ObservesTheRateTheGeometryAssumes. The filter size encodes
// an assumed revocation rate; this is how that assumption gets checked
// against a running cluster instead of trusted.
func TestStats_ObservesTheRateTheGeometryAssumes(t *testing.T) {
	r, advance := atClock(t, TTLMax)
	for i := range 10 {
		r.Revoke(idFor(uint64(100 + i)))
	}
	advance(10 * time.Second)

	st := r.Stats()
	if st.Total != 10 {
		t.Errorf("Total = %d, want 10", st.Total)
	}
	if st.RatePerSecond < 0.9 || st.RatePerSecond > 1.1 {
		t.Errorf("RatePerSecond = %v, want ~1.0", st.RatePerSecond)
	}
	if st.Capacity <= 0 {
		t.Error("Capacity is not reported, so the rate cannot be compared to anything")
	}
}

// TestIngest_GivesAnEntryTheLifetimeOfItsOwnGeneration replaces an
// earlier TestIngest_FailsClosedByRenewing, which pinned the opposite
// rule: that a merged entry got a full FRESH lifetime.
//
// That asymmetry was safe only for a one-shot merge. On a timer it is
// the renewal loop (GOBLIN-DIV-057), so the retraction is deliberate
// rather than drift. What replaces it is stricter and enough: an
// ingested entry lives exactly as long as it would have on the node that
// revoked it, because it is merged into the generation it belongs to.
func TestIngest_GivesAnEntryTheLifetimeOfItsOwnGeneration(t *testing.T) {
	peer, _ := atClock(t, TTLMax)
	id := idFor(5)
	peer.Revoke(id)

	local, advance := atClock(t, TTLMax)
	if err := local.Ingest(peer.Snapshot()); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !local.IsRevoked(id) {
		t.Fatal("ingested entry not visible")
	}

	advance(TTLMax + time.Second)
	if !local.IsRevoked(id) {
		t.Error("an ingested entry did not get a full period of life")
	}
}
