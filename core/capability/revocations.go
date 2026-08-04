// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package capability

import (
	"fmt"
	"hash/fnv"
	"sync"
	"time"
)

// Bloom filter geometry. 8192 bits (1 KiB) with 4 probes holds ~850
// entries under a 1% false-positive rate.
//
// THE SIZE IS A CLAIM ABOUT A RATE, not about a total. Entries live for
// one rotation period (see below), so 850 per period at 300s is ~2.8
// revocations per second sustained - generous when the only production
// revoker is instance migration. Stats reports the observed rate so that
// assumption can be checked against a real cluster rather than trusted.
//
// False positives fail CLOSED: a valid token is refused, never the
// reverse. That is the right direction, but it is not free - a saturated
// filter refuses valid tokens, which is why rotation exists.
const (
	bloomBits   = 8192
	bloomProbes = 4
)

// generations kept live: the current one plus the previous. An entry
// therefore survives between one and two rotation periods, and never
// less than one - which is the property correctness depends on.
const generations = 2

// DefaultRotationPeriod is how long a generation accumulates before it
// is retired.
//
// It MUST NOT be shorter than the longest token TTL. Rotating faster
// would forget a revocation while the token it revokes is still valid,
// which is a security hole rather than a tuning choice. TTLMax is that
// bound, so the period is defined from it rather than written as a
// number somebody has to keep in sync.
const DefaultRotationPeriod = TTLMax

// Revocations is the revocation set: a generational Bloom filter.
//
// The set-only-grows design it replaced could not forget, and nothing
// ever called Revoke in production, so the defect was dormant - an empty
// filter has a 0% false-positive rate forever. Wiring a producer is
// exactly what would have started filling it, and at 2000 entries it
// refuses 15% of VALID tokens; at 4000, 54%. The missing producer was
// masking the missing rotation (GOBLIN-DIV-015).
type Revocations struct {
	mu sync.RWMutex

	// gens[0] is current, gens[1] the previous generation. Lookups check
	// every generation; inserts land in gens[0] only.
	gens [][]byte

	period    time.Duration
	genStart  time.Time
	genCount  int    // inserts into the current generation
	total     uint64 // inserts over this process's lifetime
	firstSeen time.Time

	now func() time.Time
}

// NewRevocations creates an empty filter rotating at the default period.
func NewRevocations() *Revocations {
	return NewRevocationsWithPeriod(DefaultRotationPeriod)
}

// NewRevocationsWithPeriod creates an empty filter with an explicit
// rotation period. A period below TTLMax is raised to it: a caller
// asking to forget revocations faster than tokens expire is asking for
// a hole, so the floor is enforced rather than documented.
func NewRevocationsWithPeriod(period time.Duration) *Revocations {
	if period < TTLMax {
		period = TTLMax
	}
	r := &Revocations{
		gens:   make([][]byte, generations),
		period: period,
		now:    time.Now,
	}
	for i := range r.gens {
		r.gens[i] = make([]byte, bloomBits/8)
	}
	r.genStart = r.now()
	r.firstSeen = r.genStart
	return r
}

func bloomPositions(id []byte) [bloomProbes]uint32 {
	var out [bloomProbes]uint32
	for i := 0; i < bloomProbes; i++ {
		h := fnv.New64a()
		_, _ = h.Write([]byte{byte(i)})
		_, _ = h.Write(id)
		out[i] = uint32(h.Sum64() % bloomBits)
	}
	return out
}

// rotateLocked retires generations whose period has elapsed. The caller
// holds the write lock.
//
// It loops rather than shifting once: a node idle for several periods
// must not need several Revoke calls to catch up, or the first entry
// after a quiet spell would land beside stale bits.
func (r *Revocations) rotateLocked() {
	for r.now().Sub(r.genStart) >= r.period {
		copy(r.gens[1:], r.gens[:len(r.gens)-1])
		r.gens[0] = make([]byte, bloomBits/8)
		r.genStart = r.genStart.Add(r.period)
		r.genCount = 0
	}
}

// Revoke marks a token id revoked.
func (r *Revocations) Revoke(tokenID []byte) {
	pos := bloomPositions(tokenID)
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rotateLocked()
	for _, p := range pos {
		r.gens[0][p/8] |= 1 << (p % 8)
	}
	r.genCount++
	r.total++
}

// IsRevoked reports whether a token id is (probabilistically) revoked.
//
// Every live generation is consulted, so an entry remains visible for at
// least one full period after it was recorded - never less than the
// longest token TTL.
func (r *Revocations) IsRevoked(tokenID []byte) bool {
	pos := bloomPositions(tokenID)
	r.mu.Lock()
	defer r.mu.Unlock()

	// Rotation happens on the read path too. A node that revokes nothing
	// still has to retire generations, or a quiet node keeps refusing
	// tokens revoked hours ago.
	r.rotateLocked()

	for _, gen := range r.gens {
		hit := true
		for _, p := range pos {
			if gen[p/8]&(1<<(p%8)) == 0 {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// Snapshot returns the current generation's bytes.
//
// It exists for anti-entropy - a periodic full-state exchange that
// repairs revocations a best-effort delta broadcast dropped - and has no
// producer today. It deliberately does NOT flatten every generation
// together: merging a flattened snapshot into a peer's current
// generation would give every entry in it a fresh full lifetime, so
// repeated exchanges between two nodes would keep resurrecting entries
// that should have aged out.
func (r *Revocations) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotateLocked()
	return append([]byte(nil), r.gens[0]...)
}

// Ingest merges a peer's snapshot into the CURRENT generation.
//
// Merging into current is deliberate and fails closed: an ingested entry
// gets at least a full period of life, so a revocation can be extended
// but never dropped. The cost is that an entry a peer was about to
// retire is renewed here, which refuses a token that had already
// expired - harmless, because an expired token is invalid anyway.
//
// Generation identity is NOT on the wire yet. Once anti-entropy exists,
// this needs to carry (generation, filter) so a stale generation can be
// refused rather than renewed; that is the anti-entropy entry's problem,
// recorded here so it is not rediscovered.
func (r *Revocations) Ingest(raw []byte) error {
	if len(raw) != bloomBits/8 {
		return fmt.Errorf("revocation snapshot is %d bytes, want %d", len(raw), bloomBits/8)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rotateLocked()
	for i, b := range raw {
		r.gens[0][i] |= b
	}
	return nil
}

// Stats reports what the filter is actually carrying.
//
// The geometry above encodes an ASSUMED revocation rate. This is how the
// assumption gets checked against a running cluster instead of trusted:
// RatePerSecond over a long uptime is the number the size should have
// been chosen from.
type Stats struct {
	CurrentGeneration int           // entries in the current generation
	Total             uint64        // entries since process start
	Uptime            time.Duration // how long this filter has existed
	RatePerSecond     float64       // observed, not assumed
	Capacity          int           // entries per generation at ~1% false positives
	Period            time.Duration
}

// Stats returns a snapshot of the filter's load.
func (r *Revocations) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotateLocked()

	up := r.now().Sub(r.firstSeen)
	var rate float64
	if up > 0 {
		rate = float64(r.total) / up.Seconds()
	}
	return Stats{
		CurrentGeneration: r.genCount,
		Total:             r.total,
		Uptime:            up,
		RatePerSecond:     rate,
		Capacity:          bloomCapacity,
		Period:            r.period,
	}
}

// bloomCapacity is the entries-per-generation the geometry sustains at
// roughly a 1% false-positive rate: n = -m*ln(2)^2/ln(p).
const bloomCapacity = 854
