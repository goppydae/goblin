package consensus

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Authenticating a snapshot's operator key registry (GOBLIN-DIV-047).
//
// GOBLIN-DIV-043 closed the outsider door: raft peers present verified
// client certificates, so a stranger cannot send InstallSnapshot. A
// MEMBER still could. Every guard the registry has - the empty-registry
// seed rule, the last-key refusal, the prev_serial replay guard,
// signature verification - runs inside Apply, and InstallSnapshot does
// not go through Apply. A node holding a valid peer certificate could
// become leader, forge a snapshot naming its own key, and install it as
// the cluster's root of trust on every replica in one step.
//
// No amount of transport hardening reaches this, which is why the entry
// excludes it as an exit: the forged snapshot arrives over an
// authenticated connection exactly like an honest one. The two are
// distinguishable only by evidence the snapshot itself carries.
//
// So the snapshot carries the registry's PROVENANCE - the founding seed
// and every signed change since - and this file replays it against a root
// the node already holds. The replay reproduces the Apply path step for
// step; if the result is not byte-identical to what the snapshot claims,
// the snapshot is refused.
//
// # Why the seed must equal the node's configured keys
//
// The seed is unsigned. It has to be: it is authorized by the registry
// being empty, and at that moment there is no key to sign against. That
// makes the seed the one link a forger can write freely, so anchoring on
// anything weaker than equality fails. Anchoring on "some key I trust
// appears in the chain" accepts a forged seed of {honest key, attacker
// key} - internally consistent, trivially valid, and fatal.
//
// The consequence is deliberate and operator-decided (2026-08-02):
// --operator-key names the cluster's FOUNDING seed set, not "keys I would
// like registered". The registry still rotates freely above that root,
// but the root itself is immutable for the cluster's life, and a node
// joining after a rotation is configured with the founding keys.

var (
	// ErrSnapshotRegistryUnverifiable means the snapshot carries operator
	// keys and this node holds no root to check them against.
	ErrSnapshotRegistryUnverifiable = errors.New(
		"snapshot carries an operator key registry and this node has no configured operator keys to authenticate it against")

	// ErrSnapshotRegistryUnprovenanced means the snapshot carries a
	// registry with no provenance - the shape every snapshot had before
	// GOBLIN-DIV-047.
	ErrSnapshotRegistryUnprovenanced = errors.New(
		"snapshot carries an operator key registry with no provenance")

	// ErrSnapshotSeedMismatch means the snapshot's founding key set is not
	// this node's configured root of trust.
	ErrSnapshotSeedMismatch = errors.New(
		"snapshot's founding operator key set is not this node's configured root of trust")

	// ErrSnapshotChainInvalid means the recorded provenance does not
	// replay: a signature failed, an ordering guard failed, or the result
	// disagreed with the registry the snapshot claims.
	ErrSnapshotChainInvalid = errors.New("snapshot operator key provenance does not replay")
)

// verifyOperatorRegistry authenticates a snapshot's registry against this
// node's configured roots and returns the verified key set and serial.
//
// It takes the snapshot's claimed registry as `claimed` and re-derives the
// registry independently; the claim is only ever compared, never trusted.
// Callers must not hold f.mu: this reads trustedRoots, which is immutable
// after construction, and touches no other FSM state.
func (f *FSM) verifyOperatorRegistry(
	claimed map[string]*goblinv1.OperatorKey,
	claimedSerial uint64,
	seedRaw []byte,
	chainRaw [][]byte,
) error {
	// An empty registry is self-authenticating: it authorizes nothing, so
	// there is nothing for a forger to gain and nothing to verify. This is
	// also what keeps a cluster that never seeded any keys working exactly
	// as before - the documented fail-closed mode.
	if len(claimed) == 0 {
		if len(seedRaw) != 0 || len(chainRaw) != 0 {
			return fmt.Errorf("%w: registry is empty but provenance is not", ErrSnapshotChainInvalid)
		}
		if claimedSerial != 0 {
			return fmt.Errorf("%w: registry is empty at serial %d", ErrSnapshotChainInvalid, claimedSerial)
		}
		return nil
	}

	// A node that trusts nothing may not adopt a root of trust from the
	// wire. Refusing is the fail-closed answer and it is the whole reason
	// this check can be honest: were it permissive, an attacker would
	// simply target an unconfigured follower and the property would hold
	// only for nodes that opted in (operator decision, 2026-08-02).
	if len(f.trustedRoots) == 0 {
		return ErrSnapshotRegistryUnverifiable
	}

	if len(seedRaw) == 0 {
		// Every snapshot written before this entry lands has this shape.
		// Refused with the same remedy the pre-schema-reset JSON snapshot
		// gives, because it is the same situation: state on disk that this
		// binary cannot authenticate.
		return fmt.Errorf("%w: written before GOBLIN-DIV-047; wipe the data dir and rejoin",
			ErrSnapshotRegistryUnprovenanced)
	}

	var seed goblinv1.OperatorKeySeed
	if err := proto.Unmarshal(seedRaw, &seed); err != nil {
		return fmt.Errorf("%w: undecodable seed: %w", ErrSnapshotChainInvalid, err)
	}

	// Replay the seed exactly as applyOperatorKeySeed does.
	registry := make(map[string]*goblinv1.OperatorKey, len(seed.GetKeys()))
	for _, k := range seed.GetKeys() {
		if err := capability.ValidateOperatorKey(k); err != nil {
			return fmt.Errorf("%w: seed key: %w", ErrSnapshotChainInvalid, err)
		}
		if _, dup := registry[k.GetKeyId()]; dup {
			return fmt.Errorf("%w: seed key %s appears twice", ErrSnapshotChainInvalid, k.GetKeyId())
		}
		registry[k.GetKeyId()] = k
	}
	if len(registry) == 0 {
		return fmt.Errorf("%w: seed carries no keys", ErrSnapshotChainInvalid)
	}

	// THE ANCHOR. Everything else in this function is arithmetic; this is
	// the line that makes it authentication.
	if !sameKeySet(registry, f.trustedRoots) {
		return fmt.Errorf("%w: snapshot founded on %d key(s), this node is configured with %d",
			ErrSnapshotSeedMismatch, len(registry), len(f.trustedRoots))
	}
	serial := uint64(1) // the seed bumps the serial exactly once

	// Replay each signed change against the registry as it stood at that
	// point - the same resolver contract Apply uses, so a change signed by
	// a key that had already been removed fails here as it would have
	// there.
	for i, raw := range chainRaw {
		var chg goblinv1.OperatorKeyChange
		if err := proto.Unmarshal(raw, &chg); err != nil {
			return fmt.Errorf("%w: undecodable change %d: %w", ErrSnapshotChainInvalid, i, err)
		}
		resolve := capability.OperatorKeyResolver(func(keyID string) (ed25519.PublicKey, bool) {
			k, ok := registry[keyID]
			if !ok {
				return nil, false
			}
			return ed25519.PublicKey(append([]byte(nil), k.GetPublicKey()...)), true
		})
		payload, err := capability.VerifyOperatorKeyChange(&chg, resolve)
		if err != nil {
			return fmt.Errorf("%w: change %d: %w", ErrSnapshotChainInvalid, i, err)
		}
		if payload.GetPrevSerial() != serial {
			return fmt.Errorf("%w: change %d signed against serial %d, replay is at %d",
				ErrSnapshotChainInvalid, i, payload.GetPrevSerial(), serial)
		}

		switch payload.GetOp() {
		case goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD:
			k := payload.GetKey()
			if verr := capability.ValidateOperatorKey(k); verr != nil {
				return fmt.Errorf("%w: change %d add: %w", ErrSnapshotChainInvalid, i, verr)
			}
			if _, exists := registry[k.GetKeyId()]; exists {
				// Apply treats this as a no-op and does not bump the
				// serial, so it never enters the chain. Finding one here
				// means the provenance was assembled by something other
				// than the Apply path.
				return fmt.Errorf("%w: change %d re-adds registered key %s, which Apply would not have recorded",
					ErrSnapshotChainInvalid, i, k.GetKeyId())
			}
			registry[k.GetKeyId()] = k

		case goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE:
			id := payload.GetKey().GetKeyId()
			if _, exists := registry[id]; !exists {
				return fmt.Errorf("%w: change %d removes unregistered key %s", ErrSnapshotChainInvalid, i, id)
			}
			if len(registry) == 1 {
				return fmt.Errorf("%w: change %d would empty the registry", ErrSnapshotChainInvalid, i)
			}
			delete(registry, id)

		default:
			return fmt.Errorf("%w: change %d has op %v", ErrSnapshotChainInvalid, i, payload.GetOp())
		}
		serial++
	}

	// The claim must equal what the evidence produces. Without this the
	// chain would be decoration: a forger could append an honest chain to
	// a dishonest operator_keys map and every check above would pass.
	if !sameKeySet(registry, claimed) {
		return fmt.Errorf("%w: replay yields %d key(s), snapshot claims %d",
			ErrSnapshotChainInvalid, len(registry), len(claimed))
	}
	if serial != claimedSerial {
		return fmt.Errorf("%w: replay yields serial %d, snapshot claims %d",
			ErrSnapshotChainInvalid, serial, claimedSerial)
	}
	return nil
}
