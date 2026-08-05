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

// DefaultSyncInterval is how often a node exchanges filters with a peer
// to repair revocations the delta broadcast dropped (GOBLIN-DIV-057).
//
// Anti-entropy converges eventually rather than instantly, so this
// interval IS the exposure window for a node that missed a delta - it
// replaces the TTLMax-long window that node would otherwise carry. It
// must therefore stay well inside a token lifetime, which is why it is
// derived from TTLMax rather than written as a number somebody has to
// keep in sync: a tenth of the lifetime gives ten repair opportunities
// before the token in question expires, so no single lost round trip
// decides the outcome.
//
// A constant, not configuration. Every non-null default in this repo
// has cost a defect recently; make it configurable when someone needs
// it to be, not in advance.
const DefaultSyncInterval = TTLMax / 10

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

	period time.Duration

	// genIndex is the ABSOLUTE index of gens[0], floor(unix / period).
	// Deriving it from the wall clock rather than from construction time
	// is what lets two nodes agree on which generation a filter belongs
	// to without coordinating - the property anti-entropy needs and
	// process-relative generations cannot give (GOBLIN-DIV-057).
	//
	// Signed, matching the Unix seconds it derives from. An unsigned
	// index would buy nothing and would put a sign conversion on the one
	// path where underflow silently means "the far future".
	genIndex int64

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
	r.firstSeen = r.now()
	r.genIndex = r.indexAt(r.firstSeen)
	return r
}

// indexAt is the generation index covering an instant: the absolute
// window number every node computes identically for the same instant.
//
// The period is at least TTLMax by construction, so the divisor is never
// zero. A clock before the epoch is clamped rather than allowed to wrap
// an unsigned index.
func (r *Revocations) indexAt(t time.Time) int64 {
	secs := t.Unix()
	if secs < 0 {
		secs = 0
	}
	return secs / int64(r.period/time.Second)
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

// rotateLocked retires generations the wall clock has moved past. The
// caller holds the write lock.
//
// A node idle for several periods must catch up in ONE call, or the
// first entry after a quiet spell would land beside stale bits. Shifting
// by the whole distance at once also bounds the work: a clock that jumps
// forward by years costs the same as one that jumps by two periods,
// where a loop would not terminate in any useful time.
//
// A clock that moves BACKWARDS rotates nothing. Entries then live longer
// than their period, which is the fail-closed direction: a revocation
// outliving its window refuses an expired token, while dropping one
// early would accept a live one.
func (r *Revocations) rotateLocked() {
	cur := r.indexAt(r.now())
	if cur <= r.genIndex {
		return
	}

	shift := cur - r.genIndex
	if shift >= generations {
		for i := range r.gens {
			r.gens[i] = make([]byte, bloomBits/8)
		}
	} else {
		copy(r.gens[shift:], r.gens[:int64(len(r.gens))-shift])
		for i := int64(0); i < shift; i++ {
			r.gens[i] = make([]byte, bloomBits/8)
		}
	}
	r.genIndex = cur
	r.genCount = 0
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

// Generation is one live generation on the wire: its absolute index and
// its filter bytes.
//
// The index is what makes periodic exchange safe. Without it a receiver
// can only merge into whatever is current locally, which renews every
// ingested entry; two nodes whose rotation windows are offset then keep
// handing an entry back and forth into ever-later windows and it never
// ages out (GOBLIN-DIV-057).
type Generation struct {
	Index  int64
	Filter []byte
}

// Snapshot returns EVERY live generation, each tagged with its absolute
// index.
//
// It exists for anti-entropy - a periodic full-state exchange that
// repairs revocations a best-effort delta broadcast dropped.
//
// All live generations go on the wire, not just the current one. The
// filter keeps two because an entry must outlive the longest token TTL
// wherever in the period it landed; exporting only the current one would
// repair a strictly narrower window than the filter itself maintains,
// and a revocation made late in a window would become invisible to peers
// the moment that window rolled - while the token it revoked was still
// valid. Sending them separately rather than flattened is what keeps
// this safe: each generation carries the index that fixes its lifetime.
func (r *Revocations) Snapshot() []Generation {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotateLocked()

	out := make([]Generation, 0, len(r.gens))
	for slot, gen := range r.gens {
		if int64(slot) > r.genIndex {
			break // pre-epoch clock; no such absolute generation exists
		}
		out = append(out, Generation{
			Index:  r.genIndex - int64(slot),
			Filter: append([]byte(nil), gen...),
		})
	}
	return out
}

// Ingest merges a peer's generations into the local generations with the
// SAME absolute index.
//
// An entry therefore keeps the lifetime it was given when it was
// revoked, no matter how many times it is exchanged: a generation the
// receiver has already retired is dropped rather than renewed, and one
// from the future is ignored because the receiver will compute that
// index itself when the clock reaches it.
func (r *Revocations) Ingest(gens []Generation) error {
	for _, g := range gens {
		if len(g.Filter) != bloomBits/8 {
			return fmt.Errorf("revocation filter for generation %d is %d bytes, want %d",
				g.Index, len(g.Filter), bloomBits/8)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.rotateLocked()

	for _, g := range gens {
		if g.Index > r.genIndex {
			continue // from the future; this node reaches it on its own clock
		}
		slot := r.genIndex - g.Index
		if slot >= generations {
			continue // already retired here
		}
		for i, b := range g.Filter {
			r.gens[slot][i] |= b
		}
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
