// Package hlc is a minimal hybrid logical clock (DDR-4): timestamps
// order last-writer-wins updates (the gossip locator layer) even when
// physical clocks disagree. A node merges every remote timestamp it
// sees, so its next local stamp is always after anything it observed.
package hlc

import (
	"sync"
	"time"
)

// Timestamp is a hybrid logical timestamp. Ordering is (Wall, Counter,
// Node) - the node id is a deterministic tiebreak so the order is
// total across the cluster.
type Timestamp struct {
	Wall    int64  `json:"wall"`    // physical component, unix nanoseconds
	Counter uint32 `json:"counter"` // logical component within one Wall value
	Node    string `json:"node"`    // minting node, tiebreak
}

// After reports whether t is strictly later than o.
func (t Timestamp) After(o Timestamp) bool {
	if t.Wall != o.Wall {
		return t.Wall > o.Wall
	}
	if t.Counter != o.Counter {
		return t.Counter > o.Counter
	}
	return t.Node > o.Node
}

// IsZero reports an unset timestamp.
func (t Timestamp) IsZero() bool {
	return t.Wall == 0 && t.Counter == 0 && t.Node == ""
}

// Clock mints monotonically increasing timestamps for one node.
type Clock struct {
	mu   sync.Mutex
	node string
	phys func() int64
	last Timestamp
}

// New creates a clock backed by the system clock.
func New(node string) *Clock {
	return NewWithSource(node, func() int64 { return time.Now().UnixNano() })
}

// NewWithSource creates a clock with an injected physical source;
// tests freeze it to drive ordering deterministically.
func NewWithSource(node string, phys func() int64) *Clock {
	return &Clock{node: node, phys: phys}
}

// Now mints the next timestamp: physical time when it has advanced past
// everything seen, else the logical counter increments.
func (c *Clock) Now() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.phys()
	if now > c.last.Wall {
		c.last = Timestamp{Wall: now, Node: c.node}
	} else {
		c.last = Timestamp{Wall: c.last.Wall, Counter: c.last.Counter + 1, Node: c.node}
	}
	return c.last
}

// Observe merges a remote timestamp: local time never falls behind
// anything this node has seen.
func (c *Clock) Observe(remote Timestamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if remote.After(c.last) {
		c.last = Timestamp{Wall: remote.Wall, Counter: remote.Counter, Node: c.node}
	}
}
