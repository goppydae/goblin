// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package migration

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// Applier is the consensus surface a proposer needs: commit bytes and
// report the FSM's response. Declared here rather than imported so this
// package does not depend on the consensus package, which would make a
// cycle once consensus grows a migration read path.
//
// ApplyWithResponse rather than Apply, because the FSM's refusals -
// concurrent migration, stale epoch, missing rights - come back as the
// Apply RESPONSE, not as a transport error. A proposer that only
// checked the transport would read every rejection as success.
type Applier interface {
	ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error)
}

// RaftProposer commits migration records through consensus. It
// satisfies Proposer.
type RaftProposer struct {
	raft    Applier
	timeout time.Duration
}

// NewRaftProposer wires a production Proposer. A zero timeout takes the
// default rather than meaning "wait forever": a migration blocked on an
// unreachable quorum should fail and roll back, not hang holding a
// stopped process.
func NewRaftProposer(raft Applier, timeout time.Duration) *RaftProposer {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &RaftProposer{raft: raft, timeout: timeout}
}

// ProposeMigrateBegin commits the intent to migrate.
func (p *RaftProposer) ProposeMigrateBegin(_ context.Context, mb *goblinv1.MigrateBegin) error {
	return p.apply(&goblinv1.LogEntry{
		Type:    goblinv1.CommandType_COMMAND_TYPE_MIGRATE_BEGIN,
		Payload: &goblinv1.LogEntry_MigrateBegin{MigrateBegin: mb},
	}, "MIGRATE_BEGIN")
}

// ProposeMigrateCommit commits the outcome.
func (p *RaftProposer) ProposeMigrateCommit(_ context.Context, mc *goblinv1.MigrateCommit) error {
	return p.apply(&goblinv1.LogEntry{
		Type:    goblinv1.CommandType_COMMAND_TYPE_MIGRATE_COMMIT,
		Payload: &goblinv1.LogEntry_MigrateCommit{MigrateCommit: mc},
	}, "MIGRATE_COMMIT")
}

func (p *RaftProposer) apply(entry *goblinv1.LogEntry, what string) error {
	data, err := proto.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", what, err)
	}
	resp, err := p.raft.ApplyWithResponse(data, p.timeout)
	if err != nil {
		return fmt.Errorf("committing %s: %w", what, err)
	}
	// The FSM reports refusals through the response. Surfacing it as an
	// error is what makes a rejected concurrent migration visible to the
	// coordinator instead of looking like a successful begin.
	if applyErr, ok := resp.(error); ok && applyErr != nil {
		return fmt.Errorf("%s rejected by the FSM: %w", what, applyErr)
	}
	return nil
}
