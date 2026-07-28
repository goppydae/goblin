package consensus

import (
	"bytes"
	"errors"
	"io"
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"
)

func mustApply(t *testing.T, f *FSM, entry *goblinv1.LogEntry) interface{} {
	t.Helper()
	data, err := proto.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return f.Apply(&raft.Log{Data: data})
}

func TestFSM_SetIncrementsVersion(t *testing.T) {
	f := NewFSM()

	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v1"),
	})
	if resp != nil {
		t.Fatalf("SET response = %v, want nil", resp)
	}

	val, ver, ok := f.GetWithVersion("ns", "k")
	if !ok || string(val) != "v1" || ver != 1 {
		t.Fatalf("GetWithVersion = (%q, %d, %v), want (v1, 1, true)", val, ver, ok)
	}

	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v2"),
	})
	_, ver, _ = f.GetWithVersion("ns", "k")
	if ver != 2 {
		t.Fatalf("version after second SET = %d, want 2", ver)
	}
}

func TestFSM_CASAppliesOnVersionMatch(t *testing.T) {
	f := NewFSM()
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v1"),
	})

	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "k",
		Value: []byte("v2"), CasVersion: 1,
	})
	if resp != nil {
		t.Fatalf("CAS(match) response = %v, want nil", resp)
	}

	val, ver, _ := f.GetWithVersion("ns", "k")
	if string(val) != "v2" || ver != 2 {
		t.Fatalf("after CAS = (%q, %d), want (v2, 2)", val, ver)
	}
}

func TestFSM_CASRejectsStaleVersion(t *testing.T) {
	f := NewFSM()
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v1"),
	})
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v2"),
	})

	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "k",
		Value: []byte("stale-write"), CasVersion: 1,
	})
	err, ok := resp.(error)
	if !ok || !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("CAS(stale) response = %v, want ErrCASMismatch", resp)
	}

	val, ver, _ := f.GetWithVersion("ns", "k")
	if string(val) != "v2" || ver != 2 {
		t.Fatalf("state after rejected CAS = (%q, %d), want unchanged (v2, 2)", val, ver)
	}
}

func TestFSM_CASOnMissingKey(t *testing.T) {
	f := NewFSM()

	// Version 0 = "key must not exist yet": create-if-absent.
	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "new",
		Value: []byte("v1"), CasVersion: 0,
	})
	if resp != nil {
		t.Fatalf("CAS(create) response = %v, want nil", resp)
	}
	_, ver, ok := f.GetWithVersion("ns", "new")
	if !ok || ver != 1 {
		t.Fatalf("after create-CAS version = %d (ok=%v), want 1", ver, ok)
	}

	// Nonzero expected version on a missing key is a mismatch.
	resp = mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "absent",
		Value: []byte("x"), CasVersion: 3,
	})
	if err, ok := resp.(error); !ok || !errors.Is(err, ErrCASMismatch) {
		t.Fatalf("CAS(absent, v3) response = %v, want ErrCASMismatch", resp)
	}
}

func TestFSM_UnknownCommandRejected(t *testing.T) {
	f := NewFSM()
	resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType(99), Namespace: "ns", Key: "k",
	})
	if _, ok := resp.(error); !ok {
		t.Fatalf("unknown command response = %v, want error (silent no-ops are a consistency hazard)", resp)
	}
}

// fakeSink is an in-memory raft.SnapshotSink.
type fakeSink struct {
	bytes.Buffer
	cancelled bool
}

func (s *fakeSink) ID() string    { return "fake" }
func (s *fakeSink) Cancel() error { s.cancelled = true; return nil }
func (s *fakeSink) Close() error  { return nil }

func TestFSM_SnapshotRestoreCarriesVersions(t *testing.T) {
	f := NewFSM()
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v1"),
	})
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "k",
		Value: []byte("v2"), CasVersion: 1,
	})

	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	restored := NewFSM()
	if err := restored.Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	val, ver, ok := restored.GetWithVersion("ns", "k")
	if !ok || string(val) != "v2" || ver != 2 {
		t.Fatalf("restored = (%q, %d, %v), want (v2, 2, true)", val, ver, ok)
	}

	// CAS against the restored version must behave exactly as pre-snapshot.
	resp := mustApply(t, restored, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "k",
		Value: []byte("v3"), CasVersion: 2,
	})
	if resp != nil {
		t.Fatalf("CAS after restore = %v, want nil", resp)
	}
}

func TestFSM_RestoreLegacySnapshot(t *testing.T) {
	// Pre-versioning snapshots are a bare JSON state map. Existing keys
	// restore with version 1 (they exist, so "must not exist" CAS-create
	// semantics may not fire for them).
	legacy := []byte(`{"ns":{"k":"djE="}}` + "\n") // "v1" base64
	f := NewFSM()
	if err := f.Restore(io.NopCloser(bytes.NewReader(legacy))); err != nil {
		t.Fatalf("Restore(legacy): %v", err)
	}
	val, ver, ok := f.GetWithVersion("ns", "k")
	if !ok || string(val) != "v1" || ver != 1 {
		t.Fatalf("legacy restore = (%q, %d, %v), want (v1, 1, true)", val, ver, ok)
	}
}
