// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// seeded returns an FSM with one registered key and that key's private
// half, which is the only thing that can authorize a change.
func seeded(t *testing.T) (*FSM, *goblinv1.OperatorKey, ed25519.PrivateKey) {
	t.Helper()
	f := NewFSM(nil)
	root, priv := opKey(t, "root")
	if err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{root},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return f, root, priv
}

// signedChange builds a change signed by priv on behalf of authorizer.
func signedChange(t *testing.T, op goblinv1.OperatorKeyOp, key *goblinv1.OperatorKey,
	prevSerial uint64, authorizer string, priv ed25519.PrivateKey) *goblinv1.OperatorKeyChange {
	t.Helper()
	chg, err := capability.SignOperatorKeyChange(&goblinv1.OperatorKeyChangePayload{
		Op:               op,
		Key:              key,
		PrevSerial:       prevSerial,
		AuthorizingKeyId: authorizer,
	}, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}
	return chg
}

func TestAddAuthorizedByARegisteredKeySucceeds(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")

	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("authorized add: %v", err)
	}
	keys, newSerial := f.OperatorKeysLocal()
	if len(keys) != 2 {
		t.Fatalf("registry holds %d keys after the add, want 2", len(keys))
	}
	if newSerial != serial+1 {
		t.Fatalf("serial = %d after the add, want %d", newSerial, serial+1)
	}
}

func TestAddAuthorizedByAnUnregisteredKeyIsRefused(t *testing.T) {
	f, _, _ := seeded(t)
	_, serial := f.OperatorKeysLocal()
	stranger, strangerPriv := opKey(t, "stranger")
	victim, _ := opKey(t, "victim")

	// The stranger signs correctly - the signature is valid - but the
	// key it names is not in the registry. This is the negative the
	// design's testing standard asks for by name.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, victim, serial,
		stranger.GetKeyId(), strangerPriv,
	)).(error)
	if !errors.Is(err, capability.ErrOperatorKeyUnknown) {
		t.Fatalf("add signed by an unregistered key = %v, want ErrOperatorKeyUnknown", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused add changed the registry (now %d keys)", f.OperatorKeyCountLocal())
	}
}

func TestAddWithAForgedSignatureIsRefused(t *testing.T) {
	f, root, _ := seeded(t)
	_, serial := f.OperatorKeysLocal()
	_, wrongPriv := opKey(t, "wrong")
	second, _ := opKey(t, "second")

	// Names the registered root as authorizer, but signs with a
	// different private key.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), wrongPriv,
	)).(error)
	if !errors.Is(err, capability.ErrOperatorKeySignature) {
		t.Fatalf("add with a forged signature = %v, want ErrOperatorKeySignature", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused add changed the registry (now %d keys)", f.OperatorKeyCountLocal())
	}
}

func TestReplayedChangeIsRefused(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")
	chg := signedChange(t, goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD,
		second, serial, root.GetKeyId(), priv)

	if err, _ := f.applyOperatorKeyChange(chg).(error); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Byte-identical replay of a change that already landed.
	err, _ := f.applyOperatorKeyChange(chg).(error)
	if !errors.Is(err, ErrOperatorRegistryStale) {
		t.Fatalf("replayed change = %v, want ErrOperatorRegistryStale", err)
	}
}

func TestRemoveAuthorizedByARegisteredKeySucceeds(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, serial = f.OperatorKeysLocal()

	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: second.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry holds %d keys after the remove, want 1", f.OperatorKeyCountLocal())
	}
	if _, ok := f.resolveOperatorKeyLocked(second.GetKeyId()); ok {
		t.Fatalf("removed key %s still resolves", second.GetKeyId())
	}
}

func TestRemovingTheLastKeyIsRefused(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()

	// This is the invariant that makes "an empty registry may be seeded"
	// safe: no log entry can drive a populated registry back to empty,
	// so the seed path is reachable exactly once.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: root.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error)
	if !errors.Is(err, ErrOperatorLastKey) {
		t.Fatalf("removing the last key = %v, want ErrOperatorLastKey", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry emptied itself (now %d keys)", f.OperatorKeyCountLocal())
	}
}

func TestChangeWithUnspecifiedOpIsRefused(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")

	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_UNSPECIFIED, second, serial,
		root.GetKeyId(), priv,
	)).(error)
	if err == nil {
		t.Fatal("a change with an unspecified op was applied; want a refusal")
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused change altered the registry (now %d keys)", f.OperatorKeyCountLocal())
	}
}

func TestChangeAgainstAnEmptyRegistryIsRefused(t *testing.T) {
	f := NewFSM(nil)
	stranger, priv := opKey(t, "stranger")
	victim, _ := opKey(t, "victim")

	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, victim, 0, stranger.GetKeyId(), priv,
	)).(error)
	if !errors.Is(err, ErrOperatorRegistryEmpty) {
		t.Fatalf("change against an empty registry = %v, want ErrOperatorRegistryEmpty", err)
	}
}

// TestApplyDispatchesTheChangeCommand uses mustApply (defined in
// fsm_test.go), not a separate helper: the brief's draft named this
// applyEntry, but that duplicates mustApply's job for no reason and the
// task instructions call for using the existing helper instead of adding
// a second one that does the same thing.
func TestApplyDispatchesTheChangeCommand(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")

	if err, _ := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_CHANGE,
		Payload: &goblinv1.LogEntry_OperatorKeyChange{
			OperatorKeyChange: signedChange(t,
				goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial,
				root.GetKeyId(), priv),
		},
	}).(error); err != nil {
		t.Fatalf("apply change entry: %v", err)
	}
	if f.OperatorKeyCountLocal() != 2 {
		t.Fatalf("registry holds %d keys after the log entry, want 2", f.OperatorKeyCountLocal())
	}
}

// TestChangeCommandWithMismatchedPayloadIsRefused proves applyOperatorKeyChange
// cannot panic on a nil payload. The schema cannot force LogEntry.Type to
// agree with the populated oneof case, and proto3 getters return nil on a
// mismatch, so a CHANGE-typed entry can arrive carrying a different
// payload entirely.
func TestChangeCommandWithMismatchedPayloadIsRefused(t *testing.T) {
	f, _, _ := seeded(t)
	// Type says CHANGE; the oneof carries a migration. GetOperatorKeyChange()
	// returns nil and the FSM must refuse rather than panic.
	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_CHANGE,
		Payload: &goblinv1.LogEntry_MigrateCommit{
			MigrateCommit: &goblinv1.MigrateCommit{},
		},
	})
	if err, _ := resp.(error); err == nil {
		t.Fatal("a CHANGE command with a mismatched oneof payload was accepted")
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused change altered the registry (now %d keys)", f.OperatorKeyCountLocal())
	}
}

// TestRegistryCannotBeEmptiedByRepeatedRemoval proves the last-key rule
// survives an indirect route: removing keys down to one, then trying to
// remove that survivor, must still be refused. The direct case (refusing
// to remove the sole key of a freshly seeded registry) is covered by
// TestRemovingTheLastKeyIsRefused; this proves a registry that REACHED one
// key via removal is not treated any differently.
func TestRegistryCannotBeEmptiedByRepeatedRemoval(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, serial = f.OperatorKeysLocal()

	// Remove down to one. Allowed.
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: second.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("remove second: %v", err)
	}
	_, serial = f.OperatorKeysLocal()

	// Now remove the survivor. This is the attack: an empty registry can be
	// re-seeded with anyone's key, so it must be unreachable.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: root.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error)
	if !errors.Is(err, ErrOperatorLastKey) {
		t.Fatalf("removing the survivor = %v, want ErrOperatorLastKey", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry reached %d keys; it must never be emptiable", f.OperatorKeyCountLocal())
	}
}

// TestSeededRegistryCannotBeReSeededByEmptyingIt states, in one test, the
// property the whole design exists for: emptying the registry to unlock
// re-seeding is not a two-step attack that a narrower test could miss -
// it is impossible at either step.
func TestSeededRegistryCannotBeReSeededByEmptyingIt(t *testing.T) {
	f, root, priv := seeded(t)
	attacker, _ := opKey(t, "attacker")
	_, serial := f.OperatorKeysLocal()

	// Step 1: try to empty it. Must fail.
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: root.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error); !errors.Is(err, ErrOperatorLastKey) {
		t.Fatalf("emptying the registry = %v, want ErrOperatorLastKey", err)
	}

	// Step 2: with it still non-empty, a seed carrying the attacker's key
	// must be refused rather than merged or substituted.
	err, _ := f.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{attacker},
	}).(error)
	if !errors.Is(err, ErrOperatorRegistrySeeded) {
		t.Fatalf("re-seeding a populated registry = %v, want ErrOperatorRegistrySeeded", err)
	}
	if _, ok := f.resolveOperatorKeyLocked(attacker.GetKeyId()); ok {
		t.Fatal("the attacker's key entered the registry")
	}
	if _, ok := f.resolveOperatorKeyLocked(root.GetKeyId()); !ok {
		t.Fatal("the original root key was displaced")
	}
}

func TestAddedKeyCanItselfAuthorizeAChange(t *testing.T) {
	f, root, rootPriv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, secondPriv := opKey(t, "second")

	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), rootPriv,
	)).(error); err != nil {
		t.Fatalf("add second: %v", err)
	}
	_, serial = f.OperatorKeysLocal()

	// The point of an ADD is that the installed key WORKS. Counting keys
	// does not prove that - an ADD that stored an empty record would keep
	// the count right and still be useless. Authorizing a further change
	// with the new key's OWN private half exercises the stored bytes and
	// the resolver wiring, which no other test in this file does.
	//
	// It does NOT pin either defensive copy: this path only READS the
	// resolved slice, so removing a copy leaves it passing. Copies can
	// only be pinned by writing through the value handed back, which is
	// what TestResolveOperatorKeyReturnsACopy does.
	third, _ := opKey(t, "third")
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, third, serial, second.GetKeyId(), secondPriv,
	)).(error); err != nil {
		t.Fatalf("a change authorized by the newly added key was refused: %v", err)
	}
	if f.OperatorKeyCountLocal() != 3 {
		t.Fatalf("registry holds %d keys, want 3", f.OperatorKeyCountLocal())
	}
}

func TestRemoveBumpsTheSerial(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, afterAdd := f.OperatorKeysLocal()

	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: second.GetKeyId()}, afterAdd, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// Existing tests re-read the serial after every step and sign against
	// whatever they read, which makes them structurally blind to a missing
	// bump. Assert it directly: without it, a change captured at afterAdd
	// stays valid once the remove has landed, and "valid at exactly one
	// serial" is no longer true.
	_, afterRemove := f.OperatorKeysLocal()
	if afterRemove != afterAdd+1 {
		t.Fatalf("serial = %d after remove, want %d", afterRemove, afterAdd+1)
	}
}

func TestRemovingAnUnregisteredKeyIsRefused(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	second, _ := opKey(t, "second")
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, second, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("add: %v", err)
	}
	_, serial = f.OperatorKeysLocal()
	stranger, _ := opKey(t, "stranger")

	// Without the exists guard this falls through to a delete that removes
	// nothing, bumps the serial, and reports success - the FSM claiming to
	// have removed a key it never held, and burning a serial doing it.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		&goblinv1.OperatorKey{KeyId: stranger.GetKeyId()}, serial, root.GetKeyId(), priv,
	)).(error)
	if err == nil {
		t.Fatal("removing an unregistered key was accepted")
	}
	if f.OperatorKeyCountLocal() != 2 {
		t.Fatalf("registry holds %d keys after a refused remove, want 2", f.OperatorKeyCountLocal())
	}
	if _, after := f.OperatorKeysLocal(); after != serial {
		t.Fatalf("a refused remove bumped the serial to %d, want %d", after, serial)
	}
}

func TestAddingAMalformedKeyIsRefused(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()
	bad, _ := opKey(t, "bad")
	bad.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"

	// A record whose id lies about its bytes must never reach the registry.
	// An unusable entry still counts toward the last-key guard, so it could
	// leave a registry that can neither authorize anything, nor be emptied,
	// nor be re-seeded - permanently bricked, and strictly worse than
	// empty, which is at least recoverable by re-bootstrapping.
	err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, bad, serial, root.GetKeyId(), priv,
	)).(error)
	if !errors.Is(err, capability.ErrOperatorKeyMalformed) {
		t.Fatalf("adding a key whose id lies about its bytes = %v, want ErrOperatorKeyMalformed", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("a refused add installed a key (now %d)", f.OperatorKeyCountLocal())
	}
	if _, after := f.OperatorKeysLocal(); after != serial {
		t.Fatalf("a refused add bumped the serial to %d, want %d", after, serial)
	}
}

func TestReAddingARegisteredKeyIsANoOpThatDoesNotBumpTheSerial(t *testing.T) {
	f, root, priv := seeded(t)
	_, serial := f.OperatorKeysLocal()

	// key_id is derived from the bytes, so a matching id guarantees
	// matching bytes - this is a true no-op, not a silent key swap. It must
	// not bump the serial: doing so would invalidate any change an operator
	// had already signed against that serial.
	if err, _ := f.applyOperatorKeyChange(signedChange(t,
		goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD, root, serial, root.GetKeyId(), priv,
	)).(error); err != nil {
		t.Fatalf("re-adding a registered key: %v", err)
	}
	if f.OperatorKeyCountLocal() != 1 {
		t.Fatalf("registry holds %d keys after a no-op add, want 1", f.OperatorKeyCountLocal())
	}
	if _, after := f.OperatorKeysLocal(); after != serial {
		t.Fatalf("a no-op add bumped the serial from %d to %d", serial, after)
	}
}
