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
	"errors"
	"io"
	"testing"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

func admitEntry(specUUID, instUUID []byte, nodeID string) *goblinv1.LogEntry {
	return &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_ADMIT,
		Payload: &goblinv1.LogEntry_Admit{Admit: &goblinv1.ApplyAdmit{
			SpecUuid: specUUID, InstanceUuid: instUUID, NodeId: nodeID,
		}},
	}
}

func transitionEntry(instUUID []byte, to goblinv1.InstanceState, reason string) *goblinv1.LogEntry {
	return &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_TRANSITION,
		Payload: &goblinv1.LogEntry_Transition{Transition: &goblinv1.InstanceTransition{
			InstanceUuid: instUUID, To: to, Reason: reason,
		}},
	}
}

func TestFSM_AdmitCreatesAdmittedInstance(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()

	if resp := mustApply(t, f, admitEntry(spec, inst, "node-1")); resp != nil {
		t.Fatalf("ADMIT response = %v, want nil", resp)
	}

	got, ok := f.GetInstance(ident.String(inst))
	if !ok {
		t.Fatal("admitted instance not found")
	}
	if got.State != goblinv1.InstanceState_INSTANCE_STATE_ADMITTED {
		t.Fatalf("state = %v, want ADMITTED", got.State)
	}
	if got.NodeId != "node-1" || ident.String(got.SpecUuid) != ident.String(spec) {
		t.Fatalf("record fields wrong: %+v", got)
	}
}

func TestFSM_AdmitRejectsDuplicateAndMalformed(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, inst, "node-1"))

	if resp := mustApply(t, f, admitEntry(spec, inst, "node-2")); resp == nil {
		t.Fatal("duplicate ADMIT accepted")
	}
	if resp := mustApply(t, f, admitEntry(spec, []byte{1, 2, 3}, "node-1")); resp == nil {
		t.Fatal("short instance UUID accepted")
	}
	if resp := mustApply(t, f, admitEntry(spec, ident.NewV7(), "")); resp == nil {
		t.Fatal("empty node id accepted")
	}
	if resp := mustApply(t, f, &goblinv1.LogEntry{Type: goblinv1.CommandType_COMMAND_TYPE_ADMIT}); resp == nil {
		t.Fatal("ADMIT with no payload accepted")
	}
}

func TestFSM_TransitionForwardLegalBackwardIllegal(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, inst, "node-1"))

	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, "")); resp != nil {
		t.Fatalf("ADMITTED->RUNNING rejected: %v", resp)
	}
	// Backward: RUNNING -> ADMITTED must be rejected.
	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_ADMITTED, "")); resp == nil {
		t.Fatal("backward transition accepted")
	}
	// Self: RUNNING -> RUNNING must be rejected.
	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, "")); resp == nil {
		t.Fatal("self transition accepted")
	}
	// Unknown instance.
	if resp := mustApply(t, f, transitionEntry(ident.NewV7(), goblinv1.InstanceState_INSTANCE_STATE_RUNNING, "")); resp == nil {
		t.Fatal("transition of unknown instance accepted")
	}
	// UNSPECIFIED target.
	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_UNSPECIFIED, "")); resp == nil {
		t.Fatal("transition to UNSPECIFIED accepted")
	}
}

func TestFSM_TerminatedTombstonesForever(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, inst, "node-1"))
	mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""))

	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, "test-kill")); resp != nil {
		t.Fatalf("RUNNING->TERMINATED rejected: %v", resp)
	}
	if !f.IsTombstoned(ident.String(inst)) {
		t.Fatal("terminated instance not tombstoned")
	}
	// The record survives TERMINATED (reason is auditable)...
	got, ok := f.GetInstance(ident.String(inst))
	if !ok || got.Reason != "test-kill" {
		t.Fatalf("terminated record = %+v, %v; want reason test-kill", got, ok)
	}
	// From TERMINATED only ARCHIVED is legal.
	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, "")); resp == nil {
		t.Fatal("resurrection from TERMINATED accepted")
	}
	// ...and ARCHIVED compacts the record but the tombstone stays.
	if resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED, "compaction")); resp != nil {
		t.Fatalf("TERMINATED->ARCHIVED rejected: %v", resp)
	}
	if _, ok := f.GetInstance(ident.String(inst)); ok {
		t.Fatal("archived record not compacted")
	}
	if !f.IsTombstoned(ident.String(inst)) {
		t.Fatal("tombstone dropped at ARCHIVED - tombstones are append-only forever")
	}

	// A tombstoned UUID can never be admitted again.
	if resp := mustApply(t, f, admitEntry(spec, inst, "node-2")); resp == nil {
		t.Fatal("tombstoned UUID re-admitted")
	}
}

func TestFSM_SignalAuthorizedAgainstRights(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, inst, "node-3"))
	mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""))

	signal := func(signum int32, rights uint64) interface{} {
		return mustApply(t, f, &goblinv1.LogEntry{
			Type: goblinv1.CommandType_COMMAND_TYPE_SIGNAL,
			Payload: &goblinv1.LogEntry_Signal{Signal: &goblinv1.SignalRequest{
				InstanceUuid: inst, Signum: signum, Rights: rights,
			}},
		})
	}

	// SIGTERM with the TERM right: authorized, response is the node id.
	if resp := signal(15, 1<<0); resp != "node-3" {
		t.Fatalf("authorized signal response = %v, want node-3", resp)
	}
	// SIGKILL with only the TERM right: rejected.
	if resp := signal(9, 1<<0); resp == nil {
		t.Fatal("under-privileged signal authorized")
	} else if _, isErr := resp.(error); !isErr {
		t.Fatalf("rejection is not an error: %v", resp)
	}
	// SIGSEGV: never grantable.
	if resp := signal(11, ^uint64(0)); resp == nil {
		t.Fatal("ungrantable signal authorized")
	}

	// Signals to terminated instances are refused.
	mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, "gone"))
	if resp := signal(15, 1<<0); resp == nil {
		t.Fatal("signal to terminated instance authorized")
	}
}

func TestFSM_SnapshotRestoreCarriesInstancesAndTombstones(t *testing.T) {
	f := NewFSM(nil)
	spec := ident.NewV7()
	live, dead := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, live, "node-1"))
	mustApply(t, f, admitEntry(spec, dead, "node-2"))
	mustApply(t, f, transitionEntry(dead, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, "died"))

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	restored := NewFSM(nil)
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	got, ok := restored.GetInstance(ident.String(live))
	if !ok || got.State != goblinv1.InstanceState_INSTANCE_STATE_ADMITTED {
		t.Fatalf("restored live instance = %+v, %v", got, ok)
	}
	if !restored.IsTombstoned(ident.String(dead)) {
		t.Fatal("tombstone lost across snapshot/restore")
	}
	if len(restored.ListInstances()) != 2 {
		t.Fatalf("restored %d instances, want 2", len(restored.ListInstances()))
	}
}

func TestLegalTransition_Table(t *testing.T) {
	S := func(n int32) goblinv1.InstanceState { return goblinv1.InstanceState(n) }
	legal := [][2]goblinv1.InstanceState{
		{S(1), S(2)}, {S(1), S(4)}, {S(4), S(7)}, {S(7), S(8)}, {S(2), S(3)},
	}
	illegal := [][2]goblinv1.InstanceState{
		{S(4), S(1)}, {S(7), S(4)}, {S(8), S(7)}, {S(4), S(4)}, {S(1), S(0)}, {S(8), S(4)},
	}
	for _, p := range legal {
		if !LegalTransition(p[0], p[1]) {
			t.Errorf("LegalTransition(%v, %v) = false, want true", p[0], p[1])
		}
	}
	for _, p := range illegal {
		if LegalTransition(p[0], p[1]) {
			t.Errorf("LegalTransition(%v, %v) = true, want false", p[0], p[1])
		}
	}
}

// ErrIllegalTransition must be distinguishable as data.
func TestFSM_IllegalTransitionTypedError(t *testing.T) {
	f := NewFSM(nil)
	spec, inst := ident.NewV7(), ident.NewV7()
	mustApply(t, f, admitEntry(spec, inst, "node-1"))
	mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""))

	resp := mustApply(t, f, transitionEntry(inst, goblinv1.InstanceState_INSTANCE_STATE_ADMITTED, ""))
	err, ok := resp.(error)
	if !ok || !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("backward transition response = %v, want ErrIllegalTransition", resp)
	}
}
