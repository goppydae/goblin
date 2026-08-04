// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"errors"
	"fmt"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// ErrIllegalTransition is returned (as the Apply response) when an
// InstanceTransition violates the lifecycle FSM. Errors are data:
// callers distinguish a rejected transition from transport failures.
var ErrIllegalTransition = errors.New("instance lifecycle: illegal transition")

// LegalTransition reports whether the instance lifecycle FSM permits
// from -> to. The lifecycle is forward-only (DDR-6): ADMITTED ->
// SCHEDULED -> STARTING -> RUNNING -> DRAINING -> STOPPING ->
// TERMINATED -> ARCHIVED, with forward skips permitted (a dispatch may
// go ADMITTED -> RUNNING directly) and TERMINATED reachable from any
// non-terminal state. Nothing leaves ARCHIVED, nothing re-enters an
// earlier state, and UNSPECIFIED is never a destination.
func LegalTransition(from, to goblinv1.InstanceState) bool {
	if to == goblinv1.InstanceState_INSTANCE_STATE_UNSPECIFIED || to == from {
		return false
	}
	switch from {
	case goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED:
		return false
	case goblinv1.InstanceState_INSTANCE_STATE_TERMINATED:
		return to == goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED
	default:
		return to > from
	}
}

// applyAdmit creates an instance record in ADMITTED state. The UUID was
// minted at propose time by the leader; Apply only records it. A UUID
// that was ever tombstoned, or is already live, is rejected: identity
// is never reused (DDR-1). Callers hold f.mu.
func (f *FSM) applyAdmit(admit *goblinv1.ApplyAdmit) interface{} {
	if admit == nil {
		return fmt.Errorf("ADMIT command with no payload")
	}
	instID := ident.String(admit.InstanceUuid)
	specID := ident.String(admit.SpecUuid)
	if instID == "" || specID == "" {
		return fmt.Errorf("ADMIT with malformed UUIDs (instance %d bytes, spec %d bytes)",
			len(admit.InstanceUuid), len(admit.SpecUuid))
	}
	if admit.NodeId == "" {
		return fmt.Errorf("ADMIT for instance %s with no placement node", instID)
	}
	if _, dead := f.tombstones[instID]; dead {
		return fmt.Errorf("ADMIT rejected: instance UUID %s is tombstoned and can never be reused", instID)
	}
	if _, exists := f.instances[instID]; exists {
		return fmt.Errorf("ADMIT rejected: instance UUID %s already admitted", instID)
	}

	f.instances[instID] = &goblinv1.AgentInstance{
		InstanceUuid: append([]byte(nil), admit.InstanceUuid...),
		SpecUuid:     append([]byte(nil), admit.SpecUuid...),
		NodeId:       admit.NodeId,
		State:        goblinv1.InstanceState_INSTANCE_STATE_ADMITTED,
		Health:       &goblinv1.HealthStatus{Status: "healthy"},
	}
	return nil
}

// applyTransition moves an instance through the lifecycle FSM.
// TERMINATED tombstones the UUID (append-only, forever - operator
// decision 2026-07-28); ARCHIVED compacts the record while the
// tombstone stays. Callers hold f.mu.
func (f *FSM) applyTransition(tr *goblinv1.InstanceTransition) interface{} {
	if tr == nil {
		return fmt.Errorf("TRANSITION command with no payload")
	}
	instID := ident.String(tr.InstanceUuid)
	if instID == "" {
		return fmt.Errorf("TRANSITION with malformed UUID (%d bytes)", len(tr.InstanceUuid))
	}
	inst, ok := f.instances[instID]
	if !ok {
		return fmt.Errorf("TRANSITION for unknown instance %s", instID)
	}
	if !LegalTransition(inst.State, tr.To) {
		return fmt.Errorf("%w: %s -> %s (instance %s)", ErrIllegalTransition, inst.State, tr.To, instID)
	}

	inst.State = tr.To
	if tr.Reason != "" {
		inst.Reason = tr.Reason
	}
	switch tr.To {
	case goblinv1.InstanceState_INSTANCE_STATE_TERMINATED:
		f.tombstones[instID] = struct{}{}
	case goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED:
		f.tombstones[instID] = struct{}{}
		delete(f.instances, instID)
	}
	return nil
}

// applySignal authorizes a signal against the rights bitmap at the FSM
// (GOBLIN-DIV-017, DDR-10): the Raft log is the audit trail, so an
// unauthorized request is rejected here and never reaches a node. The
// response on success is the target's node id - the delivery address.
// Callers hold f.mu.
func (f *FSM) applySignal(sig *goblinv1.SignalRequest) interface{} {
	if sig == nil {
		return fmt.Errorf("SIGNAL command with no payload")
	}
	instID := ident.String(sig.InstanceUuid)
	if instID == "" {
		return fmt.Errorf("SIGNAL with malformed UUID (%d bytes)", len(sig.InstanceUuid))
	}
	inst, ok := f.instances[instID]
	if !ok {
		return fmt.Errorf("SIGNAL for unknown instance %s", instID)
	}
	switch inst.State {
	case goblinv1.InstanceState_INSTANCE_STATE_RUNNING,
		goblinv1.InstanceState_INSTANCE_STATE_DRAINING,
		goblinv1.InstanceState_INSTANCE_STATE_STOPPING:
		// signalable
	default:
		return fmt.Errorf("SIGNAL rejected: instance %s is %s, not signalable", instID, inst.State)
	}

	required, err := gapicrypto.RightForSignal(sig.Signum)
	if err != nil {
		return fmt.Errorf("SIGNAL rejected for instance %s: %w", instID, err)
	}
	if sig.Rights&required != required {
		return fmt.Errorf("SIGNAL rejected for instance %s: rights %#x lack %#x", instID, sig.Rights, required)
	}
	return inst.NodeId
}

// GetInstance returns a copy of a live instance record by canonical
// UUID string.
func (f *FSM) GetInstance(instanceID string) (*goblinv1.AgentInstance, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	inst, ok := f.instances[instanceID]
	if !ok {
		return nil, false
	}
	return proto.Clone(inst).(*goblinv1.AgentInstance), true
}

// ListInstances returns copies of every live instance record.
func (f *FSM) ListInstances() []*goblinv1.AgentInstance {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*goblinv1.AgentInstance, 0, len(f.instances))
	for _, inst := range f.instances {
		out = append(out, proto.Clone(inst).(*goblinv1.AgentInstance))
	}
	return out
}

// IsTombstoned reports whether a UUID has ever been terminated.
// Tombstones are append-only forever.
func (f *FSM) IsTombstoned(instanceID string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, dead := f.tombstones[instanceID]
	return dead
}
