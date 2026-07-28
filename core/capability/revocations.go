package capability

import (
	"fmt"
	"hash/fnv"
	"sync"
)

// Bloom filter geometry: 8192 bits (1 KiB) with 4 hash probes keeps
// the false-positive rate under ~1% up to ~600 live revocations -
// generous against 60-300s token TTLs, after which revoked ids age out
// of relevance. False positives fail CLOSED (a valid token is refused,
// never the reverse).
const (
	bloomBits   = 8192
	bloomProbes = 4
)

// Revocations is the gossip-merged revocation set. Nodes Revoke
// locally, broadcast Snapshot over the bus, and Ingest snapshots from
// peers; the filter only ever grows (bitwise OR), matching the
// append-only revocation semantics.
type Revocations struct {
	mu   sync.RWMutex
	bits []byte
}

// NewRevocations creates an empty filter.
func NewRevocations() *Revocations {
	return &Revocations{bits: make([]byte, bloomBits/8)}
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

// Revoke marks a token id revoked.
func (r *Revocations) Revoke(tokenID []byte) {
	pos := bloomPositions(tokenID)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range pos {
		r.bits[p/8] |= 1 << (p % 8)
	}
}

// IsRevoked reports whether a token id is (probabilistically) revoked.
func (r *Revocations) IsRevoked(tokenID []byte) bool {
	pos := bloomPositions(tokenID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, p := range pos {
		if r.bits[p/8]&(1<<(p%8)) == 0 {
			return false
		}
	}
	return true
}

// Snapshot returns the filter bytes for gossip broadcast.
func (r *Revocations) Snapshot() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]byte(nil), r.bits...)
}

// Ingest merges a peer's snapshot (bitwise OR). A snapshot of the
// wrong geometry is rejected: merging it would corrupt the filter.
func (r *Revocations) Ingest(raw []byte) error {
	if len(raw) != bloomBits/8 {
		return fmt.Errorf("revocation snapshot is %d bytes, want %d", len(raw), bloomBits/8)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, b := range raw {
		r.bits[i] |= b
	}
	return nil
}
