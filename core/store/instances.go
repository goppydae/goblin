// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package store

import (
	"context"
	"fmt"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// Admit commits an instance admission through Raft. The caller (the
// leader's reconciler) minted the UUIDv7 and decided placement at
// propose time; the FSM records the instance in ADMITTED state and
// enforces never-reuse against the tombstone set.
func (s *Store) Admit(ctx context.Context, specUUID, instanceUUID []byte, nodeID string) error {
	cmd := &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_ADMIT,
		Payload: &goblinv1.LogEntry_Admit{Admit: &goblinv1.ApplyAdmit{
			SpecUuid:     specUUID,
			InstanceUuid: instanceUUID,
			NodeId:       nodeID,
		}},
	}
	return s.applyCommand(cmd)
}

// TransitionInstance commits a lifecycle transition through Raft. The
// FSM rejects illegal transitions (consensus.ErrIllegalTransition).
func (s *Store) TransitionInstance(ctx context.Context, instanceUUID []byte, to goblinv1.InstanceState, reason string) error {
	cmd := &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_TRANSITION,
		Payload: &goblinv1.LogEntry_Transition{Transition: &goblinv1.InstanceTransition{
			InstanceUuid: instanceUUID,
			To:           to,
			Reason:       reason,
		}},
	}
	return s.applyCommand(cmd)
}

// SignalInstance commits a signal request through Raft so it is
// authorized at the FSM and audited in the log. On success it returns
// the target's node id - the delivery address.
func (s *Store) SignalInstance(ctx context.Context, req *goblinv1.SignalRequest) (string, error) {
	cmd := &goblinv1.LogEntry{
		Type:    goblinv1.CommandType_COMMAND_TYPE_SIGNAL,
		Payload: &goblinv1.LogEntry_Signal{Signal: req},
	}
	data, err := proto.Marshal(cmd)
	if err != nil {
		return "", err
	}
	resp, err := s.consensus.ApplyWithResponse(data, s.timeout)
	if err != nil {
		return "", err
	}
	switch v := resp.(type) {
	case error:
		return "", v
	case string:
		return v, nil
	default:
		return "", fmt.Errorf("signal: unexpected FSM response %T", resp)
	}
}

// GetInstance reads a live instance record, leader-gated like Get.
func (s *Store) GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, bool, error) {
	if err := s.requireLeader(); err != nil {
		return nil, false, err
	}
	inst, ok := s.consensus.GetInstance(instanceID)
	return inst, ok, nil
}

// ListInstances reads every live instance record, leader-gated.
// MigrationInFlight reports the in-flight migration for an instance.
// A pass-through to the replicated record: the reconciler asks so it
// does not recover an instance the migration coordinator stopped on
// purpose (GOBLIN-DIV-049).
func (s *Store) MigrationInFlight(instanceUUID []byte) (*goblinv1.MigrationRecord, bool) {
	if s.consensus == nil {
		return nil, false
	}
	return s.consensus.MigrationInFlight(instanceUUID)
}

// IsTombstoned reports whether an instance UUID was ever terminated.
// A pass-through to the replicated append-only tombstone set, read by
// the orphan reaper (GOBLIN-DIV-067).
//
// Not leader-gated: it is a local FSM read, and every replica's
// tombstone set carries the same entries once the log is applied. A
// follower that answers "not tombstoned" from a lagging log only costs
// the reap it would have done on the next heartbeat.
func (s *Store) IsTombstoned(instanceID string) bool {
	if s.consensus == nil {
		return false
	}
	return s.consensus.IsTombstoned(instanceID)
}

func (s *Store) ListInstances(ctx context.Context) ([]*goblinv1.AgentInstance, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	return s.consensus.ListInstances(), nil
}

// applyCommand marshals and applies a command whose FSM response is
// nil-or-error.
func (s *Store) applyCommand(cmd *goblinv1.LogEntry) error {
	data, err := proto.Marshal(cmd)
	if err != nil {
		return err
	}
	resp, err := s.consensus.ApplyWithResponse(data, s.timeout)
	if err != nil {
		return err
	}
	if respErr, ok := resp.(error); ok {
		return respErr
	}
	return nil
}

// requireLeader gates strongly-consistent reads on verified leadership.
func (s *Store) requireLeader() error {
	if !s.consensus.IsLeader() {
		return ErrNotLeader
	}
	if err := s.consensus.VerifyLeader(); err != nil {
		return ErrNotLeader
	}
	return nil
}
