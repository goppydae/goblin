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
	"context"
	"encoding/json"
	"hash/crc64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

// plantedSnapshotMeta mirrors the unexported fileSnapshotMeta the real
// store reads back (vendor/github.com/hashicorp/raft/file_snapshot.go):
// an embedded raft.SnapshotMeta plus a CRC field, JSON-flattened to one
// object at meta.json. Building it against the real raft.SnapshotMeta
// type (rather than hand-copying field names) keeps this test honest if
// the vendored schema ever changes shape.
type plantedSnapshotMeta struct {
	raft.SnapshotMeta
	CRC []byte
}

// TestJSONSnapshotRefusedAtStartup is GOBLIN-DIV-040's demonstration (b):
// a node whose data dir holds a pre-schema-reset (JSON) FSM snapshot must
// refuse to start, with the operator-facing message reaching the
// operator - not just the FSM's internal error.
//
// The plant reproduces the exact on-disk layout FileSnapshotStore
// expects (snapshots/<id>/{meta.json,state.bin}) and a CRC64(ECMA) over
// state.bin that matches what FileSnapshotStore.Open verifies, so the
// bytes actually reach FSM.Restore instead of being rejected earlier as
// a corrupt/unreadable snapshot - that would prove nothing about the
// JSON-refusal path this test exists to check.
//
// Empirically (read, not assumed): raft.NewRaft's restoreSnapshot
// (vendor/.../raft/api.go) discards FSM.Restore's actual error and
// returns the generic "failed to load any existing snapshots"; the real
// message only ever reaches raft's internal hclog logger. That is why
// core/consensus/raft.go's NewConsensus installs a LevelWriter
// (raftLogCapture) as Raft's LogOutput and folds the last ERROR line
// back into the returned error - without it, this test would fail on
// the propagation assertions below even though the FSM correctly
// refused the snapshot.
func TestJSONSnapshotRefusedAtStartup(t *testing.T) {
	dataDir := t.TempDir()
	snapDir := filepath.Join(dataDir, "snapshots", "1-1-1")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatalf("mkdir planted snapshot dir: %v", err)
	}

	// The exact pre-reset shape core/consensus/fsm.go's looksLikeJSON
	// refuses (same payload as TestFSM_RestoreRefusesJSONSnapshot in
	// core/consensus/fsm_test.go): a top-level JSON object keyed by the
	// old SchemaVersion envelope.
	state := []byte(`{"schema_version":2,"state":{"ns":{"k":"djE="}},"versions":{"ns":{"k":1}}}`)
	if err := os.WriteFile(filepath.Join(snapDir, "state.bin"), state, 0o644); err != nil {
		t.Fatalf("write state.bin: %v", err)
	}

	// CRC64(ECMA) of state.bin, computed the same way
	// FileSnapshotStore.Open verifies it, so Open succeeds and control
	// reaches FSM.Restore.
	h := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := h.Write(state); err != nil {
		t.Fatalf("hash state.bin: %v", err)
	}

	meta := plantedSnapshotMeta{
		SnapshotMeta: raft.SnapshotMeta{
			Version: raft.SnapshotVersionMax,
			ID:      "1-1-1",
			Index:   1,
			Term:    1,
		},
		CRC: h.Sum(nil),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal planted meta.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "meta.json"), metaBytes, 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}

	addr := freeAddrs(t, 1)[0]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBinaries.goblind,
		"start",
		"--id", "refusal-test",
		"--listen-addr", addr,
		"--data", dataDir,
		"--log-format", "json",
		"--log-level", "debug",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, runErr := cmd.CombinedOutput()

	if runErr == nil {
		t.Fatalf("goblind started against a planted pre-schema-reset snapshot instead of refusing; output:\n%s", out)
	}
	exitErr, ok := runErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("goblind did not run to completion (want a clean nonzero exit): %v; output:\n%s", runErr, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("goblind exit code = 0, want nonzero; output:\n%s", out)
	}

	for _, want := range []string{"pre-schema-reset snapshot", "wipe the data dir and rejoin"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("goblind output missing %q; full output:\n%s", want, out)
		}
	}
}
