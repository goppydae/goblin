package ident

import (
	"github.com/google/uuid"
)

// namespaceGoppydae roots every name-derived identity in the silo. It
// is itself a UUIDv5 over the DNS namespace so it can be recomputed and
// audited rather than trusted as a magic literal.
var namespaceGoppydae = uuid.NewSHA1(uuid.NameSpaceDNS, []byte("goppydae"))

// Namespace returns the silo's UUIDv5 namespace as raw bytes.
func Namespace() []byte {
	b := [16]byte(namespaceGoppydae)
	return b[:]
}

// NewV5 derives a stable identity from a name. Unlike NewV7, the result
// depends on nothing but the name: every node, every cluster, and every
// restart computes the same UUID for the same name, with no store
// lookup and no leader round-trip.
//
// Use it for identities that ARE their name - a spec's operator-facing
// handle, a node's id. Do NOT use it for per-occurrence identities:
// instances and tokens must be unique per occurrence, and the lifecycle
// FSM's tombstones are append-only forever, so a re-admitted instance
// reusing a retired UUID would collide with its own tombstone. Those
// stay UUIDv7.
func NewV5(name string) []byte {
	b := [16]byte(uuid.NewSHA1(namespaceGoppydae, []byte(name)))
	return b[:]
}

// SpecUUID is the stable identity of an agent spec, derived from its
// operator-facing name.
func SpecUUID(name string) []byte { return NewV5("spec/" + name) }

// NodeUUID is the stable identity of a cluster node, derived from its
// node id. Nodes have no minted UUID of their own; this gives the
// capability scheme a scope to name them by.
func NodeUUID(nodeID string) []byte { return NewV5("node/" + nodeID) }
