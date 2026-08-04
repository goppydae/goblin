// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build cluster

package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// TestSnapshotRestoreOnLateJoin is GOBLIN-DIV-040's demonstration (a):
// a node that joins AFTER the leader has already compacted its log must
// converge by installing a Raft snapshot, not by replaying the log -
// because by the time it joins, the log entries it would need to replay
// no longer exist anywhere.
//
// Strength of the guarantee (read from vendor/github.com/hashicorp/raft,
// not assumed):
//   - snapshot.go compactLogsWithTrailing calls logs.DeleteRange, which
//     physically removes every log entry below (snapshot index -
//     TrailingLogs) from the BoltStore. With TrailingLogs=2 this deletes
//     everything but the last 2 entries.
//   - replication.go setupAppendEntries -> setPreviousLog/setNewLogs
//     call logs.GetLog on entries the follower still needs; once those
//     indexes are gone, GetLog returns ErrLogNotFound and replicateTo
//     falls back to sendLatestSnapshot (raft.go installSnapshot on the
//     receiving end calls snapshots.Create - the same call a
//     self-generated snapshot uses).
//
// A joiner starting at log index 0 against a leader that has already
// truncated below index 1 therefore has exactly one path to catch up:
// InstallSnapshot. This test does not merely wait and hope - it forces
// the precondition (waits for the LEADER's own on-disk snapshot to
// appear before the joiner ever starts) and then confirms the
// consequence (the joiner's own snapshot store gains a completed
// snapshot). That is the closest a black-box e2e can get to proving
// which code path ran without instrumenting raft itself.
func TestSnapshotRestoreOnLateJoin(t *testing.T) {
	c := &testCluster{t: t, nodes: map[string]*clusterNode{}}

	// Aggressive knobs, private to this test: threshold 8 and trailing 2
	// are far below raft's defaults (8192 / 10240) so a handful of
	// writes forces compaction well inside the CI time budget.
	leaderDataDir := filepath.Join(t.TempDir(), "node-1-raft")
	snapArgs := []string{
		"--raft-snapshot-threshold", "8",
		"--raft-snapshot-interval", "250ms",
		"--raft-trailing-logs", "2",
		"--data", leaderDataDir,
	}

	first := c.startNodeWithArgs("node-1", "", snapArgs...)
	c.waitLeader(first, 1, 15*time.Second)

	// Drive enough writes to cross the threshold. Registering a spec is
	// one Apply (the spec record) and each replica is admitted (1
	// Apply) then transitioned to RUNNING (1 Apply): 6 replicas is at
	// least 13 log entries against a threshold of 8.
	spec := &goblinv1.AgentSpec{Name: "snap-spec", Type: "sleeper", Replicas: 6, Strategy: "spread"}
	c.register(first, spec)
	insts, _ := c.waitInstances(first, "snap-spec", 6, 20*time.Second)
	if len(insts) != 6 {
		t.Fatalf("setup: wanted 6 running instances on the leader, got %d", len(insts))
	}

	// Precondition: the leader must have actually snapshotted and
	// truncated before the joiner starts, or the joiner could converge
	// by ordinary log replay and this test would prove nothing.
	//
	// waitForCompletedSnapshot proves the snapshot was persisted (the
	// directory is renamed off ".tmp" only after FileSnapshotSink.Close
	// succeeds - file_snapshot.go). Log compaction (compactLogs,
	// snapshot.go) runs synchronously immediately afterward, in the same
	// runSnapshots goroutine call, with no further I/O beyond one
	// BoltDB DeleteRange. There is no externally observable signal for
	// "compaction finished" - raft.Stats()'s last_snapshot_index is not
	// exposed over this cluster's RPC surface, and opening the leader's
	// live BoltStore file from the test process would contend with its
	// own file lock - so this test does not have a hard synchronization
	// point proving compaction is done before node-2 starts. The settle
	// margin below, plus the fact that starting a brand-new OS process
	// (node-2) takes orders of magnitude longer than one in-process
	// DeleteRange call, makes the race practically unobservable, but it
	// is a timing argument, not a proof. If this test ever flakes by
	// showing node-2 caught up via log replay (no snapshot under its
	// data dir) rather than install, that is the seam to harden first -
	// e.g. by exposing last_snapshot_index over SchedulerRPC.
	waitForCompletedSnapshot(t, leaderDataDir, 15*time.Second)
	time.Sleep(500 * time.Millisecond)

	// Now the late joiner: fresh data dir, log index 0, starting well
	// after the leader's truncation point.
	joinerDataDir := filepath.Join(t.TempDir(), "node-2-raft")
	joinerArgs := append(append([]string{}, snapArgs[:len(snapArgs)-2]...), "--data", joinerDataDir)
	c.startNodeWithArgs("node-2", first.listenAddr, joinerArgs...)
	joined := c.waitLeader(first, 2, 20*time.Second)

	// Convergence: the joiner must see the same registered instances -
	// proof that state actually transferred, not just that it joined
	// gossip and membership.
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, err := c.instances(joined, "snap-spec")
		running := 0
		for _, in := range got {
			if in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
				running++
			}
		}
		if err == nil && running == 6 && len(got) == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cluster never converged to 6 running instances after join (err: %v, got: %v)",
				err, describeInstances(got))
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The mechanical proof: the joiner's own snapshot store has a
	// completed snapshot on disk. Per the mechanism documented above,
	// that file can only exist because InstallSnapshot ran on this
	// node - it never had a local log deep enough to snapshot from
	// itself in this window.
	waitForCompletedSnapshot(t, joinerDataDir, 15*time.Second)
}

// waitForCompletedSnapshot polls a Raft data dir for a finalized (not
// ".tmp") snapshot directory containing both meta.json and state.bin -
// the on-disk layout raft.FileSnapshotStore commits to
// (vendor/github.com/hashicorp/raft/file_snapshot.go).
func waitForCompletedSnapshot(t *testing.T, dataDir string, timeout time.Duration) {
	t.Helper()
	snapRoot := filepath.Join(dataDir, "snapshots")
	deadline := time.Now().Add(timeout)
	for {
		entries, err := os.ReadDir(snapRoot)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
					continue
				}
				meta := filepath.Join(snapRoot, e.Name(), "meta.json")
				state := filepath.Join(snapRoot, e.Name(), "state.bin")
				if _, merr := os.Stat(meta); merr != nil {
					continue
				}
				if _, serr := os.Stat(state); serr != nil {
					continue
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no completed snapshot appeared under %s within %s (last list err: %v)", snapRoot, timeout, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}
