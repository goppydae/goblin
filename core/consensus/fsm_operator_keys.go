// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// The operator key registry (GOBLIN-DIV-015 piece 1).
//
// Everything in this file runs inside FSM Apply, which constrains it
// hard: no wall clock, no per-node configuration, no randomness. Every
// decision must be a pure function of replicated state and the log
// entry's bytes, or replicas diverge.
//
// The trust chain has exactly one root: OPERATOR_KEY_SEED is authorized
// by the registry being EMPTY. That sounds like a hole - anyone can seed
// an empty registry - and it is closed by two facts that hold together.
// First, a cluster with an empty registry authorizes no mutation at all,
// so there is nothing to steal by seeding one. Second, no log entry can
// drive a non-empty registry back to empty: removing the last key is
// refused. The seed is therefore reachable exactly once in a cluster's
// life, at bootstrap, which is the only time it means anything.
//
// Restore used to be the hole in this, and GOBLIN-DIV-047 closed it.
// The boundary is still worth stating plainly, because it moved rather
// than vanished.
//
// A snapshot is not a signed object and cannot be: it is machine-produced
// state, and no operator is present to sign it. So authenticating it
// cannot mean checking a signature ON the snapshot - it means checking
// the PROVENANCE the snapshot carries. Every mutation the registry ever
// took is a signed change, and the seed that started it is compared
// against the keys this node was configured with. core/consensus/
// fsm_snapshot_trust.go replays that chain on restore and refuses unless
// it reproduces the registry the snapshot claims, so a hostile raft peer
// can no longer bypass the rules in this file by shipping a snapshot
// instead of a log entry.
//
// What that does NOT cover is the rest of the snapshot. Key/value state,
// instances and migrations are still installed on the authority of the
// transport alone. That is deliberate: they are data the cluster already
// agreed on, whereas the registry is the authority over who may change
// that data, and only the second is a root of trust. A compromised member
// can still lie about instance state; it can no longer make itself an
// operator.

var (
	// ErrOperatorRegistryEmpty means no operator key is registered.
	// Every mutating verb refuses while this holds - fail closed is the
	// point of the registry, not a degraded mode.
	ErrOperatorRegistryEmpty = errors.New("operator key registry is empty: no mutation is authorized")

	// ErrOperatorRegistrySeeded means a seed arrived for a registry that
	// is already populated with a different set. Two nodes configured
	// with different root keys is a misconfiguration we refuse to paper
	// over by merging them: quietly unioning the sets would widen the
	// trust root without anyone deciding to.
	ErrOperatorRegistrySeeded = errors.New("operator key registry already seeded with a different key set")

	// ErrOperatorRegistryStale means a signed change named a registry
	// serial that is no longer current. This is the replay guard: a
	// change is valid at exactly one serial, so re-submitting a captured
	// one fails once any other change has landed.
	ErrOperatorRegistryStale = errors.New("operator key change: stale registry serial")

	// ErrOperatorLastKey means a remove would empty the registry.
	// Refusing keeps the operator from locking the cluster out of its
	// own control plane, and is what guarantees the seed path stays
	// reachable exactly once.
	ErrOperatorLastKey = errors.New("operator key change: refusing to remove the last registered key")
)

// applyOperatorKeySeed installs the configured root-of-trust keys.
// Callers hold f.mu.
//
// Re-seeding an identical set is a successful no-op so that a node
// restart does not fail its boot, and it deliberately does NOT bump the
// serial: bumping it would invalidate any signed change already in
// flight, turning every restart into a spurious refusal.
func (f *FSM) applyOperatorKeySeed(seed *goblinv1.OperatorKeySeed) interface{} {
	if seed == nil {
		return fmt.Errorf("OPERATOR_KEY_SEED command with no payload")
	}
	keys := seed.GetKeys()
	if len(keys) == 0 {
		return fmt.Errorf("%w: OPERATOR_KEY_SEED carries no keys", ErrOperatorRegistryEmpty)
	}

	// Validate the whole set before touching state: a seed is atomic, so
	// one bad record must not leave a half-installed registry.
	incoming := make(map[string]*goblinv1.OperatorKey, len(keys))
	for _, k := range keys {
		if err := capability.ValidateOperatorKey(k); err != nil {
			return fmt.Errorf("OPERATOR_KEY_SEED rejected: %w", err)
		}
		if _, dup := incoming[k.GetKeyId()]; dup {
			return fmt.Errorf("OPERATOR_KEY_SEED rejected: key %s appears twice", k.GetKeyId())
		}
		incoming[k.GetKeyId()] = k
	}

	if len(f.operatorKeys) > 0 {
		if sameKeySet(f.operatorKeys, incoming) {
			return nil // idempotent restart
		}
		return fmt.Errorf("%w: registry holds %d key(s), seed carries %d",
			ErrOperatorRegistrySeeded, len(f.operatorKeys), len(incoming))
	}

	installed := make(map[string]*goblinv1.OperatorKey, len(incoming))
	for id, k := range incoming {
		installed[id] = &goblinv1.OperatorKey{
			KeyId:     k.GetKeyId(),
			PublicKey: append([]byte(nil), k.GetPublicKey()...),
			Comment:   k.GetComment(),
		}
	}
	f.operatorKeys = installed
	f.operatorSerial++
	// Record the founding set beside the registry it created. Only this
	// branch records: the idempotent-restart path above returns without
	// touching state, so a re-seed must not append a second seed that a
	// replay would then apply twice (GOBLIN-DIV-047).
	f.operatorSeed = &goblinv1.OperatorKeySeed{Keys: cloneOperatorKeys(keys)}
	return nil
}

// cloneOperatorKeys deep-copies a key slice. The provenance records must
// not alias the caller's memory: a log entry's message is not ours to
// retain, and a later mutation of it would silently rewrite history that
// Restore is going to verify against.
func cloneOperatorKeys(in []*goblinv1.OperatorKey) []*goblinv1.OperatorKey {
	out := make([]*goblinv1.OperatorKey, 0, len(in))
	for _, k := range in {
		out = append(out, &goblinv1.OperatorKey{
			KeyId:     k.GetKeyId(),
			PublicKey: append([]byte(nil), k.GetPublicKey()...),
			Comment:   k.GetComment(),
		})
	}
	return out
}

// recordOperatorChange appends a change that ACTUALLY MUTATED the
// registry. Callers hold f.mu.
//
// "Actually mutated" is the whole contract. An ADD of an already
// registered id returns early without bumping the serial, and so does an
// idempotent re-seed; appending either would make Restore's replay take a
// step the Apply path never took, and the replay would then disagree with
// the registry it is supposed to reproduce - failing closed on honest
// state, which is the worst kind of security check.
func (f *FSM) recordOperatorChange(chg *goblinv1.OperatorKeyChange) {
	f.operatorChain = append(f.operatorChain, &goblinv1.OperatorKeyChange{
		Payload:   append([]byte(nil), chg.GetPayload()...),
		Signature: append([]byte(nil), chg.GetSignature()...),
	})
}

// sameKeySet compares two registries by key id and key bytes. Comments
// are excluded: they are operator-facing labels, not authentication
// material, and a relabelled key is the same key.
func sameKeySet(a, b map[string]*goblinv1.OperatorKey) bool {
	if len(a) != len(b) {
		return false
	}
	for id, ka := range a {
		kb, ok := b[id]
		if !ok || !bytes.Equal(ka.GetPublicKey(), kb.GetPublicKey()) {
			return false
		}
	}
	return true
}

// resolveOperatorKeyLocked maps a key id to its public key. It takes NO
// lock; the caller must already hold f.mu. The name carries that because
// a comment cannot fail a build and this one is load-bearing: the map
// read here races FSM Apply on any goroutine that has not taken the
// lock, and a torn read of the trust root is not a bug that announces
// itself.
//
// The lock cannot be taken here. The one permitted caller,
// applyOperatorKeyChange, runs inside FSM.Apply under f.mu.Lock(), and
// sync.RWMutex is not reentrant: RLock while the same goroutine holds
// Lock deadlocks. A read path that wants this data must go through
// Consensus.OperatorKeysVerified (leadership-checked) or, when a stale
// answer is provably safe, OperatorKeysLocal - not through here.
// TestResolveOperatorKeyLockedHasExactlyOneCaller enforces that
// mechanically.
//
// It is the resolver the signed-change path verifies against, so it must
// read replicated state and nothing else.
//
// The key is copied, for the same reason OperatorKeysLocal and
// MigrationInFlight copy: a caller that could write through this slice
// would corrupt the registry on one replica only, and a divergent
// registry means divergent accept/reject verdicts on signed changes -
// the exact failure this design exists to prevent.
func (f *FSM) resolveOperatorKeyLocked(keyID string) (ed25519.PublicKey, bool) {
	k, ok := f.operatorKeys[keyID]
	if !ok {
		return nil, false
	}
	return ed25519.PublicKey(append([]byte(nil), k.GetPublicKey()...)), true
}

// OperatorKeysLocal returns THIS REPLICA's applied registry sorted by
// key id, plus the current serial. The Local suffix is the contract, not
// decoration: an FSM knows only what it has applied, so a follower that
// has not yet applied an OPERATOR_KEY_CHANGE answers from the registry
// as it was before the change - including a remove. Callers for whom a
// stale yes is wrong (anything that authorizes on key material, e.g. a
// mint path) must use Consensus.OperatorKeysVerified instead. Callers
// for whom a stale answer can only fail closed may use this and must say
// at the call site why.
func (f *FSM) OperatorKeysLocal() ([]*goblinv1.OperatorKey, uint64) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	out := make([]*goblinv1.OperatorKey, 0, len(f.operatorKeys))
	for _, k := range f.operatorKeys {
		// Copied: a caller must not be able to mutate FSM state through
		// a read, which would diverge this replica from the others.
		out = append(out, &goblinv1.OperatorKey{
			KeyId:     k.GetKeyId(),
			PublicKey: append([]byte(nil), k.GetPublicKey()...),
			Comment:   k.GetComment(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetKeyId() < out[j].GetKeyId() })
	return out, f.operatorSerial
}

// OperatorKeyCountLocal reports how many operator keys THIS REPLICA has
// applied. Zero is the fail-closed condition every mutating verb checks,
// and the count is the one reading that is safe to take locally: a
// replica behind the seed reads zero and refuses, and a seeded registry
// can never return to empty because removing the last key is refused. So
// this can be stale only in the direction that refuses.
func (f *FSM) OperatorKeyCountLocal() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.operatorKeys)
}

// applyOperatorKeyChange applies one signed add or remove. Callers hold
// f.mu.
//
// The order is: registry non-empty, signature verified against
// REPLICATED key material, serial current, then the op. Nothing in the
// payload is trusted before the signature passes, and the op itself is
// inside the signature so an add cannot be flipped into a remove
// without invalidating it.
func (f *FSM) applyOperatorKeyChange(chg *goblinv1.OperatorKeyChange) interface{} {
	if chg == nil {
		return fmt.Errorf("OPERATOR_KEY_CHANGE command with no payload")
	}
	if len(f.operatorKeys) == 0 {
		return fmt.Errorf("%w: cannot authorize a change", ErrOperatorRegistryEmpty)
	}

	payload, err := capability.VerifyOperatorKeyChange(chg,
		capability.OperatorKeyResolver(f.resolveOperatorKeyLocked))
	if err != nil {
		return fmt.Errorf("OPERATOR_KEY_CHANGE rejected: %w", err)
	}

	if payload.GetPrevSerial() != f.operatorSerial {
		return fmt.Errorf("%w: signed against %d, registry is at %d",
			ErrOperatorRegistryStale, payload.GetPrevSerial(), f.operatorSerial)
	}

	switch payload.GetOp() {
	case goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD:
		k := payload.GetKey()
		if verr := capability.ValidateOperatorKey(k); verr != nil {
			return fmt.Errorf("OPERATOR_KEY_CHANGE add rejected: %w", verr)
		}
		// Re-adding an existing id is not an error but must not be a
		// silent key swap: the id is derived from the bytes, so a
		// matching id guarantees matching bytes and this is a true
		// no-op. Bump nothing.
		if _, exists := f.operatorKeys[k.GetKeyId()]; exists {
			return nil
		}
		f.operatorKeys[k.GetKeyId()] = &goblinv1.OperatorKey{
			KeyId:     k.GetKeyId(),
			PublicKey: append([]byte(nil), k.GetPublicKey()...),
			Comment:   k.GetComment(),
		}
		f.operatorSerial++
		f.recordOperatorChange(chg)
		return nil

	case goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE:
		id := payload.GetKey().GetKeyId()
		if id == "" {
			return fmt.Errorf("OPERATOR_KEY_CHANGE remove names no key id")
		}
		if _, exists := f.operatorKeys[id]; !exists {
			return fmt.Errorf("OPERATOR_KEY_CHANGE remove: key %s is not registered", id)
		}
		if len(f.operatorKeys) == 1 {
			return fmt.Errorf("%w: %s", ErrOperatorLastKey, id)
		}
		delete(f.operatorKeys, id)
		f.operatorSerial++
		f.recordOperatorChange(chg)
		return nil

	default:
		// UNSPECIFIED lands here. Refusing to guess is the same rule
		// MIGRATE_COMMIT applies to an unspecified outcome: a command
		// that did not decode must not become a mutation by default.
		return fmt.Errorf("OPERATOR_KEY_CHANGE has op %v; refusing to guess", payload.GetOp())
	}
}
