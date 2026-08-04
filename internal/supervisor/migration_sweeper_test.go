// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// fakeMigrationRegistry is leadership plus an in-flight list, recording
// what the sweep proposed.
type fakeMigrationRegistry struct {
	mu       sync.Mutex
	leader   bool
	inFlight []*goblinv1.MigrationRecord
	applied  []*goblinv1.LogEntry
}

func (f *fakeMigrationRegistry) IsLeader() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leader
}

func (f *fakeMigrationRegistry) setLeader(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leader = v
}

func (f *fakeMigrationRegistry) MigrationsInFlight() []*goblinv1.MigrationRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

func (f *fakeMigrationRegistry) ApplyWithResponse(data []byte, _ time.Duration) (interface{}, error) {
	var e goblinv1.LogEntry
	if err := proto.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, &e)
	return nil, nil
}

func (f *fakeMigrationRegistry) proposals() []*goblinv1.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*goblinv1.LogEntry(nil), f.applied...)
}

// GOBLIN-DIV-049: a leader that inherits an in-flight migration must
// abort it.
//
// Nothing else retires these records - only a MIGRATE_COMMIT does, and
// only a leader can propose one - so a leader that died mid-move leaves
// a record forever. Since the reconciler now honours those records,
// leaving one in place would make the instance permanently
// unrecoverable: a duplicate traded for a loss.
func TestMigrationSweeperAbortsOrphansOnTakingLeadership(t *testing.T) {
	uuid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	reg := &fakeMigrationRegistry{
		inFlight: []*goblinv1.MigrationRecord{
			{InstanceUuid: uuid, TargetNodeId: "node-2", CheckpointEpoch: 7},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go runMigrationSweeper(ctx, reg, quietLogger(), 10*time.Millisecond)

	// Follower: nothing may be proposed, because a follower cannot know
	// the migration is orphaned - the leader may be running it right now.
	time.Sleep(60 * time.Millisecond)
	if got := reg.proposals(); len(got) != 0 {
		t.Fatalf("a follower proposed %d commits; only a leader may retire a record", len(got))
	}

	reg.setLeader(true)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(reg.proposals()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := reg.proposals()
	if len(got) != 1 {
		t.Fatalf("proposals = %d, want exactly 1 abort", len(got))
	}
	mc := got[0].GetMigrateCommit()
	if mc == nil {
		t.Fatalf("proposal is %v, want a MIGRATE_COMMIT", got[0].GetType())
	}
	if mc.GetOutcome() != goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED {
		t.Errorf("outcome = %v, want ABORTED", mc.GetOutcome())
	}
	if mc.GetCheckpointEpoch() != 7 {
		t.Errorf("epoch = %d, want 7 - the commit must match the record it retires",
			mc.GetCheckpointEpoch())
	}

	// And it must not keep sweeping: a leader that stays leader has
	// already cleaned up, and re-aborting would fight its own new
	// migrations.
	time.Sleep(80 * time.Millisecond)
	if again := reg.proposals(); len(again) != 1 {
		t.Errorf("sweep repeated while leadership was unchanged: %d proposals", len(again))
	}
}
