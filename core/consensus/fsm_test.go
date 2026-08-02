package consensus

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
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
	f := NewFSM(nil)

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
	f := NewFSM(nil)
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
	f := NewFSM(nil)
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
	f := NewFSM(nil)

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
	f := NewFSM(nil)
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
	f := NewFSM(nil)
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

	restored := NewFSM(nil)
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

// persistToBytes runs Snapshot+Persist through a real fakeSink and
// returns the wire bytes, the way a joining node would receive them.
func persistToBytes(t *testing.T, f *FSM) []byte {
	t.Helper()
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sink := &fakeSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if sink.cancelled {
		t.Fatalf("Persist cancelled the sink on a success path")
	}
	return sink.Bytes()
}

// TestFSM_SnapshotRestoreRoundTrip builds an FSM through the real Apply
// path (SET/CAS for state+versions, ADMIT+TRANSITION for an instance
// table entry and a tombstone, MIGRATE_BEGIN for an in-flight
// migration), snapshots it through a real Persist to a buffer sink, and
// restores into a fresh FSM. Every field of goblinv1.FSMSnapshot must
// survive: state, versions, instances, tombstones, migrations.
func TestFSM_SnapshotRestoreRoundTrip(t *testing.T) {
	f := NewFSM(nil)

	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "ns", Key: "k", Value: []byte("v1"),
	})
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_CAS, Namespace: "ns", Key: "k",
		Value: []byte("v2"), CasVersion: 1,
	})

	// Instance A: admitted, promoted to RUNNING, then a migration begun
	// against it - covers the instances map and the migrations map.
	specA, instA := ident.NewV7(), ident.NewV7()
	if resp := mustApply(t, f, &goblinv1.LogEntry{
		Type:    goblinv1.CommandType_COMMAND_TYPE_ADMIT,
		Payload: &goblinv1.LogEntry_Admit{Admit: &goblinv1.ApplyAdmit{SpecUuid: specA, InstanceUuid: instA, NodeId: "node-1"}},
	}); resp != nil {
		t.Fatalf("ADMIT instA: %v", resp)
	}
	if resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_TRANSITION,
		Payload: &goblinv1.LogEntry_Transition{Transition: &goblinv1.InstanceTransition{
			InstanceUuid: instA, To: goblinv1.InstanceState_INSTANCE_STATE_RUNNING,
		}},
	}); resp != nil {
		t.Fatalf("TRANSITION instA to RUNNING: %v", resp)
	}
	migrateRight, err := capability.RightForVerb(capability.VerbJobMigrate)
	if err != nil {
		t.Fatalf("RightForVerb: %v", err)
	}
	if resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_MIGRATE_BEGIN,
		Payload: &goblinv1.LogEntry_MigrateBegin{MigrateBegin: &goblinv1.MigrateBegin{
			InstanceUuid: instA, TargetNodeId: "node-2", CheckpointEpoch: 1, Rights: migrateRight,
		}},
	}); resp != nil {
		t.Fatalf("MIGRATE_BEGIN instA: %v", resp)
	}

	// Instance B: admitted, then terminated - covers the tombstone set
	// while staying in the instances map (only ARCHIVED removes it).
	specB, instB := ident.NewV7(), ident.NewV7()
	if resp := mustApply(t, f, &goblinv1.LogEntry{
		Type:    goblinv1.CommandType_COMMAND_TYPE_ADMIT,
		Payload: &goblinv1.LogEntry_Admit{Admit: &goblinv1.ApplyAdmit{SpecUuid: specB, InstanceUuid: instB, NodeId: "node-1"}},
	}); resp != nil {
		t.Fatalf("ADMIT instB: %v", resp)
	}
	if resp := mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_TRANSITION,
		Payload: &goblinv1.LogEntry_Transition{Transition: &goblinv1.InstanceTransition{
			InstanceUuid: instB, To: goblinv1.InstanceState_INSTANCE_STATE_TERMINATED,
		}},
	}); resp != nil {
		t.Fatalf("TRANSITION instB to TERMINATED: %v", resp)
	}

	wire := persistToBytes(t, f)

	restored := NewFSM(nil)
	if err := restored.Restore(io.NopCloser(bytes.NewReader(wire))); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// state + versions
	val, ver, ok := restored.GetWithVersion("ns", "k")
	if !ok || string(val) != "v2" || ver != 2 {
		t.Fatalf("restored state = (%q, %d, %v), want (v2, 2, true)", val, ver, ok)
	}

	// instances (both A and B, A still RUNNING, B TERMINATED)
	gotA, ok := restored.GetInstance(ident.String(instA))
	if !ok {
		t.Fatalf("restored instance A missing")
	}
	if gotA.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Fatalf("restored instance A state = %v, want RUNNING", gotA.State)
	}
	gotB, ok := restored.GetInstance(ident.String(instB))
	if !ok {
		t.Fatalf("restored instance B missing")
	}
	if gotB.State != goblinv1.InstanceState_INSTANCE_STATE_TERMINATED {
		t.Fatalf("restored instance B state = %v, want TERMINATED", gotB.State)
	}

	// tombstones
	if !restored.IsTombstoned(ident.String(instB)) {
		t.Fatalf("restored: instance B not tombstoned")
	}
	if restored.IsTombstoned(ident.String(instA)) {
		t.Fatalf("restored: instance A unexpectedly tombstoned")
	}

	// migrations: a second MIGRATE_BEGIN against instA must still be
	// rejected as in-flight, proving the migration record survived.
	resp := mustApply(t, restored, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_MIGRATE_BEGIN,
		Payload: &goblinv1.LogEntry_MigrateBegin{MigrateBegin: &goblinv1.MigrateBegin{
			InstanceUuid: instA, TargetNodeId: "node-3", CheckpointEpoch: 2, Rights: migrateRight,
		}},
	})
	respErr, ok := resp.(error)
	if !ok || !errors.Is(respErr, ErrMigrationInFlight) {
		t.Fatalf("second MIGRATE_BEGIN after restore = %v, want ErrMigrationInFlight", resp)
	}
}

func TestFSM_RestoreRefusesJSONSnapshot(t *testing.T) {
	// A snapshot from before the GOBLIN-DIV-040 schema reset is JSON,
	// keyed by "schema_version" the way the old encoder wrote it. It
	// must be refused outright, not dual-read: the operator wipes the
	// data dir and rejoins rather than the FSM silently upgrading it.
	preReset := []byte(`{"schema_version":2,"state":{"ns":{"k":"djE="}},"versions":{"ns":{"k":1}}}`)

	f := NewFSM(nil)
	mustApply(t, f, &goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_SET, Namespace: "untouched", Key: "k", Value: []byte("v1"),
	})

	err := f.Restore(io.NopCloser(bytes.NewReader(preReset)))
	if err == nil {
		t.Fatalf("Restore(pre-reset JSON) = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "pre-schema-reset snapshot") {
		t.Fatalf("Restore(pre-reset JSON) error = %q, want it to contain %q", err.Error(), "pre-schema-reset snapshot")
	}
	if !strings.Contains(err.Error(), "wipe the data dir and rejoin") {
		t.Fatalf("Restore(pre-reset JSON) error = %q, want it to contain %q", err.Error(), "wipe the data dir and rejoin")
	}

	// FSM state must be untouched by the failed restore.
	val, ver, ok := f.GetWithVersion("untouched", "k")
	if !ok || string(val) != "v1" || ver != 1 {
		t.Fatalf("FSM state after refused restore = (%q, %d, %v), want unchanged (v1, 1, true)", val, ver, ok)
	}
}

func TestFSM_RestoreGarbageBytesErrorsWithoutPanic(t *testing.T) {
	garbage := []byte{0xff, 0x00, 0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03}

	f := NewFSM(nil)
	err := f.Restore(io.NopCloser(bytes.NewReader(garbage)))
	if err == nil {
		t.Fatalf("Restore(garbage) = nil error, want a decode error")
	}
}
