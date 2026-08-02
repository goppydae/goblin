package consensus

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// opKey generates a keypair and its registry record.
func opKey(t *testing.T, comment string) (*goblinv1.OperatorKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rec, err := capability.NewOperatorKey(pub, comment)
	if err != nil {
		t.Fatalf("NewOperatorKey: %v", err)
	}
	return rec, priv
}

func TestSeedInstallsKeysIntoEmptyRegistry(t *testing.T) {
	f := NewFSM(nil)
	if f.OperatorKeyCountLocal() != 0 {
		t.Fatalf("a fresh FSM has %d operator keys, want 0", f.OperatorKeyCountLocal())
	}
	k1, _ := opKey(t, "one")
	k2, _ := opKey(t, "two")

	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1, k2},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	keys, serial := f.OperatorKeysLocal()
	if len(keys) != 2 {
		t.Fatalf("registry holds %d keys, want 2", len(keys))
	}
	if serial != 1 {
		t.Fatalf("registry serial = %d after one seed, want 1", serial)
	}
}

func TestSeedIsIdempotentForAnIdenticalSet(t *testing.T) {
	f := NewFSM(nil)
	k1, _ := opKey(t, "one")
	seed := &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{k1}}

	if err, _ := f.applyOperatorKeySeed(seed).(error); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	_, serialAfterFirst := f.OperatorKeysLocal()

	// A node restart re-proposes the same configured set. It must not
	// fail the boot and must not bump the serial, which would invalidate
	// a signed change already in flight.
	if err, _ := f.applyOperatorKeySeed(seed).(error); err != nil {
		t.Fatalf("re-seed with an identical set: %v", err)
	}
	keys, serial := f.OperatorKeysLocal()
	if len(keys) != 1 {
		t.Fatalf("registry holds %d keys after re-seed, want 1", len(keys))
	}
	if serial != serialAfterFirst {
		t.Fatalf("re-seed bumped the serial from %d to %d", serialAfterFirst, serial)
	}
}

func TestSeedIntoNonEmptyRegistryWithDifferentKeysIsRefused(t *testing.T) {
	f := NewFSM(nil)
	k1, _ := opKey(t, "one")
	k2, _ := opKey(t, "two")

	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1},
	}).(error); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k2},
	}).(error)
	if !errors.Is(err, ErrOperatorRegistrySeeded) {
		t.Fatalf("seeding a different set = %v, want ErrOperatorRegistrySeeded", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused seed changed the registry (now %d keys)", f.OperatorKeyCountLocal())
	}
	// Count alone would not catch a regression that swapped the
	// registry's contents for the incoming set of the same size, so
	// assert identity: the ORIGINAL key must still be the one there.
	if _, ok := f.resolveOperatorKeyLocked(k1.GetKeyId()); !ok {
		t.Fatalf("the originally seeded key %s is gone after a refused re-seed", k1.GetKeyId())
	}
	if _, ok := f.resolveOperatorKeyLocked(k2.GetKeyId()); ok {
		t.Fatalf("the refused seed's key %s was installed", k2.GetKeyId())
	}
}

// TestSeedValidatesEveryKeyBeforeInstallingAny pins the ORDER, not just
// the error. A seed is atomic: one bad record must not leave a
// half-populated registry. Every other bad-seed test passes a batch of
// exactly one, so none of them would notice a regression from
// "validate all, then install all" to "validate and install in the same
// loop" - this one puts a GOOD key ahead of the bad one, which is the
// only arrangement that can tell those two implementations apart.
func TestSeedValidatesEveryKeyBeforeInstallingAny(t *testing.T) {
	f := NewFSM(nil)
	good, _ := opKey(t, "good")
	bad, _ := opKey(t, "bad")
	bad.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{good, bad},
	}).(error)
	if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
		t.Fatalf("seed with a good key ahead of a bad one = %v, want ErrOperatorKeyMalformed", err)
	}
	if f.OperatorKeyCountLocal() != 0 {
		t.Fatalf("a refused seed installed %d key(s); validation must complete before any install",
			f.OperatorKeyCountLocal())
	}
	if _, ok := f.resolveOperatorKeyLocked(good.GetKeyId()); ok {
		t.Fatal("the valid key from a refused seed batch was installed anyway")
	}
}

func TestSeedRejectsAKeyWhoseIDLiesAboutItsBytes(t *testing.T) {
	f := NewFSM(nil)
	k1, _ := opKey(t, "one")
	k1.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1},
	}).(error)
	if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
		t.Fatalf("seed with a lying key id = %v, want ErrOperatorKeyMalformed", err)
	}
	if f.OperatorKeyCountLocal() != 0 {
		t.Fatalf("a refused seed installed %d keys", f.OperatorKeyCountLocal())
	}
}

func TestSeedWithNoKeysIsRefused(t *testing.T) {
	f := NewFSM(nil)
	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{}).(error)
	if !errors.Is(err, ErrOperatorRegistryEmpty) {
		t.Fatalf("empty seed = %v, want ErrOperatorRegistryEmpty", err)
	}
}

func TestApplyDispatchesTheSeedCommand(t *testing.T) {
	f := NewFSM(nil)
	k1, _ := opKey(t, "one")
	if err, _ := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{k1}},
		},
	}).(error); err != nil {
		t.Fatalf("apply seed entry: %v", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry holds %d keys after the log entry, want 1", f.OperatorKeyCountLocal())
	}
}

func TestSeedCommandWithMismatchedPayloadIsRefused(t *testing.T) {
	f := NewFSM(nil)
	// Type says SEED; the oneof carries a migration. GetOperatorKeySeed()
	// returns nil, and the FSM must refuse rather than panic. The schema
	// cannot prevent this pairing, so the apply path has to.
	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_MigrateCommit{
			MigrateCommit: &goblinv1.MigrateCommit{},
		},
	})
	err, _ := resp.(error)
	if err == nil {
		t.Fatal("a SEED command with a mismatched oneof payload was accepted")
	}
	if f.OperatorKeyCountLocal() != 0 {
		t.Fatalf("a refused seed installed %d keys", f.OperatorKeyCountLocal())
	}
}

// TestResolveOperatorKeyReturnsACopy pins the defensive copy in
// resolveOperatorKeyLocked. Without this test the copy is unenforced: delete
// the append and every other test still passes. It matters because
// Task 4 passes this result into signature verification, and anything
// that wrote through the returned slice would corrupt the registered
// key on one replica only - a divergent registry means divergent
// verdicts on signed changes, which is the failure this design exists
// to prevent.
func TestResolveOperatorKeyReturnsACopy(t *testing.T) {
	f := NewFSM(nil)
	root, _ := opKey(t, "root")
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{root},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, ok := f.resolveOperatorKeyLocked(root.GetKeyId())
	if !ok {
		t.Fatalf("resolveOperatorKeyLocked(%s) did not resolve", root.GetKeyId())
	}
	// Scribble on what the caller was handed.
	for i := range got {
		got[i] ^= 0xff
	}

	again, ok := f.resolveOperatorKeyLocked(root.GetKeyId())
	if !ok {
		t.Fatal("key vanished from the registry after a caller mutated its copy")
	}
	if !bytes.Equal(again, root.GetPublicKey()) {
		t.Fatal("mutating the resolved key changed FSM state; resolveOperatorKeyLocked must return a copy")
	}
}

func TestRegistrySurvivesSnapshotRestore(t *testing.T) {
	f := NewFSM(nil)
	k1, k1Priv := opKey(t, "one")
	k2, _ := opKey(t, "two")
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1, k2},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, wantSerial := f.OperatorKeysLocal()

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("persist: %v", err)
	}

	// Anchored on the SAME founding keys the snapshot was seeded with.
	// That is the contract GOBLIN-DIV-047 introduces: a restoring node
	// authenticates the registry against its own configured roots, so a
	// node with no roots - or different ones - refuses this snapshot
	// rather than adopting it.
	restored := NewFSM([]*goblinv1.OperatorKey{k1, k2})
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("restore: %v", err)
	}
	keys, serial := restored.OperatorKeysLocal()
	if len(keys) != 2 {
		t.Fatalf("restored registry holds %d keys, want 2", len(keys))
	}
	// The serial is the replay guard. A restore that forgets it would
	// let an already-applied signed change be replayed.
	if serial != wantSerial {
		t.Fatalf("restored serial = %d, want %d", serial, wantSerial)
	}
	if _, ok := restored.resolveOperatorKeyLocked(k1.GetKeyId()); !ok {
		t.Fatalf("restored registry cannot resolve %s", k1.GetKeyId())
	}

	// Counting keys and resolving an id are both blind to the key
	// MATERIAL. resolveOperatorKeyLocked returns (nil, true) for a record with
	// no public key, so a Snapshot/Restore pair that carried ids and
	// dropped bytes would satisfy every assertion above. That failure is
	// not hypothetical: every node joining past the trailing-log window
	// and every restart past the snapshot threshold goes through Restore,
	// and a registry of ids with no bytes still reports a non-zero count
	// - so the fail-closed gate stays green and mutations keep flowing
	// while every signed registry change is refused forever. Authorizing
	// a real change with k1's private half is the only assertion here
	// that touches the restored bytes.
	third, _ := opKey(t, "third")
	if cerr, _ := restored.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, third, serial, k1.GetKeyId(), k1Priv,
	)).(error); cerr != nil {
		t.Fatalf("a change authorized by a restored key was refused: %v", cerr)
	}
	if restored.OperatorKeyCountLocal() != 3 {
		t.Fatalf("restored registry holds %d keys after the add, want 3",
			restored.OperatorKeyCountLocal())
	}
}

// TestRestoreRefusesAKeyWhoseIDLiesAboutItsBytes pins the one
// invariant Restore can enforce. A snapshot is not signed and carries
// no authorization, so nothing here can stop a well-formed hostile key
// - but a record whose id does not match its bytes must never reach
// the registry, because every reader assumes ids are derived. Without
// this check that assumption held only on the Apply path.
func TestRestoreRefusesAKeyWhoseIDLiesAboutItsBytes(t *testing.T) {
	// A snapshot is built directly rather than via FSM.Snapshot: the
	// Apply path refuses these records, so the only way to produce one
	// is to forge the snapshot, which is exactly the threat.
	snapshotWith := func(t *testing.T, id string, k *goblinv1.OperatorKey) []byte {
		t.Helper()
		raw, err := proto.Marshal(k)
		if err != nil {
			t.Fatalf("marshal operator key: %v", err)
		}
		out, err := proto.Marshal(&goblinv1.FSMSnapshot{
			OperatorKeys:           map[string][]byte{id: raw},
			OperatorRegistrySerial: 1,
		})
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		return out
	}

	t.Run("id lies about its bytes", func(t *testing.T) {
		k, _ := opKey(t, "liar")
		k.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

		f := NewFSM(nil)
		err := f.Restore(io.NopCloser(bytes.NewReader(snapshotWith(t, k.GetKeyId(), k))))
		if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
			t.Fatalf("restore of a lying key id = %v, want ErrOperatorKeyMalformed", err)
		}
		if f.OperatorKeyCountLocal() != 0 {
			t.Fatalf("a refused restore installed %d key(s)", f.OperatorKeyCountLocal())
		}
	})

	t.Run("filed under the wrong map key", func(t *testing.T) {
		// The record itself is internally consistent - it would pass
		// ValidateOperatorKey - but the map files it under someone
		// else's id. Every lookup in this package is by map key, so
		// accepting this would resolve one operator's id to another
		// operator's public key.
		k, _ := opKey(t, "honest")
		other, _ := opKey(t, "other")

		f := NewFSM(nil)
		err := f.Restore(io.NopCloser(bytes.NewReader(snapshotWith(t, other.GetKeyId(), k))))
		if err == nil {
			t.Fatal("restore accepted a key filed under another key's id")
		}
		if f.OperatorKeyCountLocal() != 0 {
			t.Fatalf("a refused restore installed %d key(s)", f.OperatorKeyCountLocal())
		}
	})
}
