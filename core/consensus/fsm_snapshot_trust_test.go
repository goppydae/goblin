package consensus

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// GOBLIN-DIV-047's gate.
//
// The threat is a MEMBER, not an outsider. GOBLIN-DIV-043 already made
// raft peers present verified client certificates, so every snapshot in
// these tests is one that arrives over a perfectly authenticated
// connection - that is the point, and it is why no transport-level test
// could stand in for these. The forged snapshots below are exactly what a
// compromised node holding a valid peer certificate can construct.

// forgeSnapshot marshals an FSMSnapshot the way a hostile leader would:
// by writing the fields directly rather than going through Snapshot().
func forgeSnapshot(t *testing.T, keys []*goblinv1.OperatorKey, serial uint64,
	seed *goblinv1.OperatorKeySeed, chain []*goblinv1.OperatorKeyChange) []byte {
	t.Helper()
	opKeys := make(map[string][]byte, len(keys))
	for _, k := range keys {
		raw, err := proto.Marshal(k)
		if err != nil {
			t.Fatalf("marshal operator key: %v", err)
		}
		opKeys[k.GetKeyId()] = raw
	}
	snap := &goblinv1.FSMSnapshot{
		OperatorKeys:           opKeys,
		OperatorRegistrySerial: serial,
	}
	if seed != nil {
		raw, err := proto.Marshal(seed)
		if err != nil {
			t.Fatalf("marshal seed: %v", err)
		}
		snap.OperatorKeySeed = raw
	}
	for _, chg := range chain {
		raw, err := proto.Marshal(chg)
		if err != nil {
			t.Fatalf("marshal change: %v", err)
		}
		snap.OperatorKeyChain = append(snap.OperatorKeyChain, raw)
	}
	raw, err := proto.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return raw
}

func restoreForged(t *testing.T, f *FSM, raw []byte) error {
	t.Helper()
	return f.Restore(io.NopCloser(bytes.NewReader(raw)))
}

// THE ENTRY'S CLOSING TEST. A node holding a valid peer certificate
// forges a snapshot naming its own key as the cluster's root of trust.
// Restore must refuse it.
//
// Four shapes, because a forger picks whichever one works and a gate that
// stops only the clumsiest is not a gate.
func TestRestoreRefusesForgedRegistry(t *testing.T) {
	honest1, _ := opKey(t, "honest-one")
	honest2, _ := opKey(t, "honest-two")
	attacker, _ := opKey(t, "attacker")
	roots := []*goblinv1.OperatorKey{honest1, honest2}

	cases := []struct {
		name   string
		want   error
		build  func() []byte
		reason string
	}{
		{
			name: "bare swap: registry is the attacker's key, no provenance at all",
			want: ErrSnapshotRegistryUnprovenanced,
			build: func() []byte {
				return forgeSnapshot(t, []*goblinv1.OperatorKey{attacker}, 1, nil, nil)
			},
			reason: "the shape every pre-GOBLIN-DIV-047 snapshot has, and what Restore used to install verbatim",
		},
		{
			name: "self-seeded: attacker declares its own founding set",
			want: ErrSnapshotSeedMismatch,
			build: func() []byte {
				seed := &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{attacker}}
				return forgeSnapshot(t, []*goblinv1.OperatorKey{attacker}, 1, seed, nil)
			},
			reason: "internally consistent and trivially valid - the seed is unsigned, so nothing inside the snapshot contradicts it",
		},
		{
			name: "widened seed: honest keys PLUS the attacker's",
			want: ErrSnapshotSeedMismatch,
			build: func() []byte {
				keys := []*goblinv1.OperatorKey{honest1, honest2, attacker}
				seed := &goblinv1.OperatorKeySeed{Keys: keys}
				return forgeSnapshot(t, keys, 1, seed, nil)
			},
			reason: "THE ONE THAT DECIDES THE DESIGN: this passes any anchor weaker than set equality, " +
				"because a locally-trusted key really does appear in it",
		},
		{
			name: "honest provenance, dishonest result",
			want: ErrSnapshotChainInvalid,
			build: func() []byte {
				seed := &goblinv1.OperatorKeySeed{Keys: roots}
				return forgeSnapshot(t, []*goblinv1.OperatorKey{honest1, honest2, attacker}, 1, seed, nil)
			},
			reason: "a real seed with the attacker's key appended to the RESULT - caught only because the " +
				"replay's output is compared against the claim",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFSM(roots)
			err := restoreForged(t, f, tc.build())
			if err == nil {
				t.Fatalf("Restore ACCEPTED a forged registry (%s).\n"+
					"A node with a valid peer certificate can now install itself as the "+
					"cluster's root of trust on every replica (GOBLIN-DIV-047)", tc.reason)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("refused with %v, want %v", err, tc.want)
			}
			// Refused means refused: nothing installed.
			if n := f.OperatorKeyCountLocal(); n != 0 {
				t.Fatalf("a refused snapshot left %d key(s) installed; restore must be atomic", n)
			}
		})
	}
}

// A node configured with no operator keys cannot authenticate a registry,
// so it refuses one rather than adopting it (operator decision,
// 2026-08-02). Were this permissive, an attacker would simply target an
// unconfigured follower.
func TestRestoreRefusesRegistryWhenNodeHasNoRoots(t *testing.T) {
	k, _ := opKey(t, "one")
	seed := &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{k}}
	raw := forgeSnapshot(t, []*goblinv1.OperatorKey{k}, 1, seed, nil)

	f := NewFSM(nil)
	err := restoreForged(t, f, raw)
	if !errors.Is(err, ErrSnapshotRegistryUnverifiable) {
		t.Fatalf("unkeyed node restored a registry it cannot authenticate: %v", err)
	}
}

// An empty registry stays restorable on an unkeyed node. This is the
// documented fail-closed mode - a cluster where nobody configured a key
// authorizes nothing - and it must not become collateral damage.
func TestRestoreAcceptsEmptyRegistryWhenNodeHasNoRoots(t *testing.T) {
	raw := forgeSnapshot(t, nil, 0, nil, nil)
	f := NewFSM(nil)
	if err := restoreForged(t, f, raw); err != nil {
		t.Fatalf("empty registry refused on an unkeyed node: %v", err)
	}
}

// An honest cluster that rotated its registry round-trips: the chain is
// replayed from the founding seed, and the anchor is still the FOUNDING
// set even though one of those keys has since been removed.
//
// This is the case that makes the seed-immutability decision concrete. A
// node joining here is configured with the founding keys, not the current
// ones, and that is what lets it verify at all.
func TestRestoreAcceptsRotatedRegistryFromFoundingSeed(t *testing.T) {
	k1, k1Priv := opKey(t, "founding-one")
	k2, _ := opKey(t, "founding-two")
	k3, _ := opKey(t, "added-later")
	roots := []*goblinv1.OperatorKey{k1, k2}

	f := NewFSM(roots)
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{Keys: roots}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Add a third key, then retire one of the founding keys.
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, k3, 1, k1.GetKeyId(), k1Priv)).(error); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE, k2, 2, k1.GetKeyId(), k1Priv)).(error); err != nil {
		t.Fatalf("remove: %v", err)
	}

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// A fresh node configured with the FOUNDING keys, one of which is no
	// longer in the registry it is about to receive.
	joiner := NewFSM(roots)
	if err := joiner.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("a rotated but honest registry was refused: %v", err)
	}
	keys, serial := joiner.OperatorKeysLocal()
	if len(keys) != 2 || serial != 3 {
		t.Fatalf("restored %d key(s) at serial %d, want 2 at serial 3", len(keys), serial)
	}
	if _, ok := joiner.resolveOperatorKeyLocked(k2.GetKeyId()); ok {
		t.Fatal("the retired founding key is still registered after restore")
	}

	// The provenance must survive the hop. A node that restored and then
	// snapshots for the NEXT joiner has to hand on the same evidence, or
	// the chain lives exactly one hop and then silently disappears -
	// leaving the third node unable to verify anything.
	snap2, err := joiner.Snapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	sink2 := &fakeSink{}
	if err := snap2.Persist(sink2); err != nil {
		t.Fatalf("second persist: %v", err)
	}
	third := NewFSM(roots)
	if err := third.Restore(io.NopCloser(bytes.NewReader(sink2.Bytes()))); err != nil {
		t.Fatalf("provenance did not survive a second hop: %v", err)
	}
	if third.OperatorKeyCountLocal() != 2 {
		t.Fatalf("second hop restored %d key(s), want 2", third.OperatorKeyCountLocal())
	}
}
