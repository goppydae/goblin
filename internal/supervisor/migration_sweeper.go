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
	"log/slog"
	"time"

	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/ident"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// migrationRegistry is the consensus surface the sweeper needs.
// *consensus.Consensus satisfies it. Declared narrowly here for the
// same reason the seeder and the voter declare their own: the sweep is
// testable against a real FSM without standing up Raft.
type migrationRegistry interface {
	IsLeader() bool
	MigrationsInFlight() []*goblinv1.MigrationRecord
	ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error)
}

// runMigrationSweeper aborts migrations orphaned by a leadership change
// (GOBLIN-DIV-049).
//
// WHY THIS HAS TO EXIST. Nothing else ever clears an in-flight
// migration record: only a MIGRATE_COMMIT does, and only a leader can
// propose one. So a leader that dies between recording the intent and
// committing the outcome leaves a record no surviving node retires. On
// its own that merely blocked re-migrating the instance. Once the
// reconciler started honouring those records - which is the fix for the
// duplicate this entry is about - it would also block RECOVERY of that
// instance, permanently, trading a duplicated instance for a lost one.
//
// WHY A LEADERSHIP CHANGE IS THE RIGHT TRIGGER, rather than a timeout.
// Only the leader can propose the commit. The instant leadership moves,
// any migration the previous leader had in flight is definitively
// orphaned: the old coordinator cannot finish it even if its process is
// still running, because its proposals will be refused. That is an
// invariant, not a heuristic, so the sweep needs no clock and picks no
// arbitrary number - and the entry's exit rules out exactly the
// arbitrary-number fixes.
//
// It polls rather than hooking LeaderCh, for the reason the operator
// key seeder gives: a node that is ALREADY the leader when it starts
// never sees an edge.
//
// The records are snapshotted at the moment leadership is taken, before
// this node can have started a migration of its own, so the sweep can
// never abort a live migration this leader owns.
func runMigrationSweeper(ctx context.Context, reg migrationRegistry, logger *slog.Logger, poll time.Duration) {
	if poll <= 0 {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	wasLeader := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		isLeader := reg.IsLeader()
		if isLeader && !wasLeader {
			sweepOrphanedMigrations(ctx, reg, logger)
		}
		wasLeader = isLeader
	}
}

// sweepOrphanedMigrations aborts every migration recorded as in flight
// at the moment this node took leadership.
//
// Aborting rather than resuming is the honest choice: this node has no
// image, no source connection and no idea how far the previous
// coordinator got. Clearing the record hands the instance back to the
// reconciler, which will re-place it if it is genuinely gone - and will
// leave it alone if it is not.
func sweepOrphanedMigrations(ctx context.Context, reg migrationRegistry, logger *slog.Logger) {
	orphans := reg.MigrationsInFlight()
	if len(orphans) == 0 {
		return
	}
	logger.LogAttrs(ctx, slog.LevelWarn,
		"took leadership with migrations in flight; aborting them as orphaned",
		slog.Int("count", len(orphans)))

	for _, rec := range orphans {
		instID := ident.String(rec.GetInstanceUuid())
		err := migration.NewRaftProposer(applierFunc(reg.ApplyWithResponse), 10*time.Second).
			ProposeMigrateCommit(ctx, &goblinv1.MigrateCommit{
				InstanceUuid:    rec.GetInstanceUuid(),
				CheckpointEpoch: rec.GetCheckpointEpoch(),
				Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED,
			})
		if err != nil {
			// Reported, not retried. The next leadership change sweeps
			// again, and a record that cannot be aborted is a fault to
			// surface rather than to spin on.
			logger.LogAttrs(ctx, slog.LevelError,
				"could not abort an orphaned migration",
				logattr.InstanceID(instID), logattr.Err(err))
			continue
		}
		logger.LogAttrs(ctx, slog.LevelWarn, "aborted an orphaned migration",
			logattr.InstanceID(instID),
			slog.String("target_node", rec.GetTargetNodeId()),
			slog.Uint64("epoch", rec.GetCheckpointEpoch()))
	}
}

// applierFunc adapts a bare ApplyWithResponse to migration.Applier.
type applierFunc func(data []byte, timeout time.Duration) (interface{}, error)

func (f applierFunc) ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error) {
	return f(data, timeout)
}
