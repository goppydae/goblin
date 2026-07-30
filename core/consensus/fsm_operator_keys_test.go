package consensus

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io"
	"testing"

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
	f := NewFSM()
	if f.OperatorKeyCount() != 0 {
		t.Fatalf("a fresh FSM has %d operator keys, want 0", f.OperatorKeyCount())
	}
	k1, _ := opKey(t, "one")
	k2, _ := opKey(t, "two")

	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1, k2},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	keys, serial := f.OperatorKeys()
	if len(keys) != 2 {
		t.Fatalf("registry holds %d keys, want 2", len(keys))
	}
	if serial != 1 {
		t.Fatalf("registry serial = %d after one seed, want 1", serial)
	}
}

func TestSeedIsIdempotentForAnIdenticalSet(t *testing.T) {
	f := NewFSM()
	k1, _ := opKey(t, "one")
	seed := &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{k1}}

	if err, _ := f.applyOperatorKeySeed(seed).(error); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	_, serialAfterFirst := f.OperatorKeys()

	// A node restart re-proposes the same configured set. It must not
	// fail the boot and must not bump the serial, which would invalidate
	// a signed change already in flight.
	if err, _ := f.applyOperatorKeySeed(seed).(error); err != nil {
		t.Fatalf("re-seed with an identical set: %v", err)
	}
	keys, serial := f.OperatorKeys()
	if len(keys) != 1 {
		t.Fatalf("registry holds %d keys after re-seed, want 1", len(keys))
	}
	if serial != serialAfterFirst {
		t.Fatalf("re-seed bumped the serial from %d to %d", serialAfterFirst, serial)
	}
}

func TestSeedIntoNonEmptyRegistryWithDifferentKeysIsRefused(t *testing.T) {
	f := NewFSM()
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
	if f.OperatorKeyCount() != 1 {
		t.Fatalf("a refused seed changed the registry (now %d keys)", f.OperatorKeyCount())
	}
	// Count alone would not catch a regression that swapped the
	// registry's contents for the incoming set of the same size, so
	// assert identity: the ORIGINAL key must still be the one there.
	if _, ok := f.resolveOperatorKey(k1.GetKeyId()); !ok {
		t.Fatalf("the originally seeded key %s is gone after a refused re-seed", k1.GetKeyId())
	}
	if _, ok := f.resolveOperatorKey(k2.GetKeyId()); ok {
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
	f := NewFSM()
	good, _ := opKey(t, "good")
	bad, _ := opKey(t, "bad")
	bad.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{good, bad},
	}).(error)
	if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
		t.Fatalf("seed with a good key ahead of a bad one = %v, want ErrOperatorKeyMalformed", err)
	}
	if f.OperatorKeyCount() != 0 {
		t.Fatalf("a refused seed installed %d key(s); validation must complete before any install",
			f.OperatorKeyCount())
	}
	if _, ok := f.resolveOperatorKey(good.GetKeyId()); ok {
		t.Fatal("the valid key from a refused seed batch was installed anyway")
	}
}

func TestSeedRejectsAKeyWhoseIDLiesAboutItsBytes(t *testing.T) {
	f := NewFSM()
	k1, _ := opKey(t, "one")
	k1.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1},
	}).(error)
	if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
		t.Fatalf("seed with a lying key id = %v, want ErrOperatorKeyMalformed", err)
	}
	if f.OperatorKeyCount() != 0 {
		t.Fatalf("a refused seed installed %d keys", f.OperatorKeyCount())
	}
}

func TestSeedWithNoKeysIsRefused(t *testing.T) {
	f := NewFSM()
	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{}).(error)
	if !errors.Is(err, ErrOperatorRegistryEmpty) {
		t.Fatalf("empty seed = %v, want ErrOperatorRegistryEmpty", err)
	}
}

func TestApplyDispatchesTheSeedCommand(t *testing.T) {
	f := NewFSM()
	k1, _ := opKey(t, "one")
	if err, _ := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: []*goblinv1.OperatorKey{k1}},
		},
	}).(error); err != nil {
		t.Fatalf("apply seed entry: %v", err)
	}
	if f.OperatorKeyCount() != 1 {
		t.Fatalf("registry holds %d keys after the log entry, want 1", f.OperatorKeyCount())
	}
}

func TestSeedCommandWithMismatchedPayloadIsRefused(t *testing.T) {
	f := NewFSM()
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
	if f.OperatorKeyCount() != 0 {
		t.Fatalf("a refused seed installed %d keys", f.OperatorKeyCount())
	}
}

// TestResolveOperatorKeyReturnsACopy pins the defensive copy in
// resolveOperatorKey. Without this test the copy is unenforced: delete
// the append and every other test still passes. It matters because
// Task 4 passes this result into signature verification, and anything
// that wrote through the returned slice would corrupt the registered
// key on one replica only - a divergent registry means divergent
// verdicts on signed changes, which is the failure this design exists
// to prevent.
func TestResolveOperatorKeyReturnsACopy(t *testing.T) {
	f := NewFSM()
	root, _ := opKey(t, "root")
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{root},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, ok := f.resolveOperatorKey(root.GetKeyId())
	if !ok {
		t.Fatalf("resolveOperatorKey(%s) did not resolve", root.GetKeyId())
	}
	// Scribble on what the caller was handed.
	for i := range got {
		got[i] ^= 0xff
	}

	again, ok := f.resolveOperatorKey(root.GetKeyId())
	if !ok {
		t.Fatal("key vanished from the registry after a caller mutated its copy")
	}
	if !bytes.Equal(again, root.GetPublicKey()) {
		t.Fatal("mutating the resolved key changed FSM state; resolveOperatorKey must return a copy")
	}
}

func TestRegistrySurvivesSnapshotRestore(t *testing.T) {
	f := NewFSM()
	k1, _ := opKey(t, "one")
	k2, _ := opKey(t, "two")
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{k1, k2},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, wantSerial := f.OperatorKeys()

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("persist: %v", err)
	}

	restored := NewFSM()
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("restore: %v", err)
	}
	keys, serial := restored.OperatorKeys()
	if len(keys) != 2 {
		t.Fatalf("restored registry holds %d keys, want 2", len(keys))
	}
	// The serial is the replay guard. A restore that forgets it would
	// let an already-applied signed change be replayed.
	if serial != wantSerial {
		t.Fatalf("restored serial = %d, want %d", serial, wantSerial)
	}
	if _, ok := restored.resolveOperatorKey(k1.GetKeyId()); !ok {
		t.Fatalf("restored registry cannot resolve %s", k1.GetKeyId())
	}
}
