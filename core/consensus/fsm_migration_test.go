package consensus

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

var (
	migInstA = []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	migInstB = []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	migSpec  = []byte{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
)

// runningInstance admits an instance and drives it to RUNNING, which is
// the only state a migration may begin from.
func runningInstance(t *testing.T, f *FSM, uuid []byte, node string) {
	t.Helper()
	if err, _ := f.applyAdmit(&goblinv1.ApplyAdmit{
		SpecUuid: migSpec, InstanceUuid: uuid, NodeId: node,
	}).(error); err != nil {
		t.Fatalf("admit: %v", err)
	}
	for _, to := range []goblinv1.InstanceState{
		goblinv1.InstanceState_INSTANCE_STATE_SCHEDULED,
		goblinv1.InstanceState_INSTANCE_STATE_STARTING,
		goblinv1.InstanceState_INSTANCE_STATE_RUNNING,
	} {
		if err, _ := f.applyTransition(&goblinv1.InstanceTransition{
			InstanceUuid: uuid, To: to,
		}).(error); err != nil {
			t.Fatalf("transition to %v: %v", to, err)
		}
	}
}

// mustInstance reads an instance through the exported accessor, which
// is what external callers see; poking the map directly would test a
// representation rather than the interface.
func mustInstance(t *testing.T, f *FSM, uuid []byte) *goblinv1.AgentInstance {
	t.Helper()
	inst, ok := f.GetInstance(ident.String(uuid))
	if !ok {
		t.Fatalf("instance %s not found", ident.String(uuid))
	}
	return inst
}

func migrateBegin(uuid []byte, target string, epoch uint64) *goblinv1.MigrateBegin {
	return &goblinv1.MigrateBegin{
		InstanceUuid:    uuid,
		TargetNodeId:    target,
		CheckpointEpoch: epoch,
		Rights:          capability.RightJobMigrate,
	}
}

func TestMigrateBeginRecordsIntentWithoutChangingState(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")

	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 1)).(error); err != nil {
		t.Fatalf("MIGRATE_BEGIN: %v", err)
	}

	rec, ok := f.MigrationInFlight(migInstA)
	if !ok {
		t.Fatal("no migration recorded")
	}
	if rec.GetSourceNodeId() != "node-1" || rec.GetTargetNodeId() != "node-2" {
		t.Errorf("record has source=%q target=%q, want node-1 -> node-2",
			rec.GetSourceNodeId(), rec.GetTargetNodeId())
	}

	// The whole design point: migration is a locator event, so the
	// lifecycle FSM must not have moved.
	inst := mustInstance(t, f, migInstA)
	if inst.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("instance is %v during migration, want RUNNING", inst.State)
	}
	if inst.NodeId != "node-1" {
		t.Errorf("node_id moved to %q before commit", inst.NodeId)
	}
}

func TestConcurrentMigrationRejected(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")

	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 1)).(error); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-3", 2)).(error)
	if !errors.Is(err, ErrMigrationInFlight) {
		t.Fatalf("want ErrMigrationInFlight for a second begin, got %v", err)
	}

	// The first migration must be untouched by the rejected second.
	rec, _ := f.MigrationInFlight(migInstA)
	if rec.GetTargetNodeId() != "node-2" {
		t.Errorf("rejected begin overwrote the in-flight target: %q", rec.GetTargetNodeId())
	}
}

func TestMigrateCommitCompletedMovesNode(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 5)).(error); err != nil {
		t.Fatalf("begin: %v", err)
	}

	if err, _ := f.applyMigrateCommit(&goblinv1.MigrateCommit{
		InstanceUuid:    migInstA,
		CheckpointEpoch: 5,
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED,
	}).(error); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if inst := mustInstance(t, f, migInstA); inst.NodeId != "node-2" {
		t.Errorf("node_id = %q after completed migration, want node-2", inst.NodeId)
	}
	if _, still := f.MigrationInFlight(migInstA); still {
		t.Error("migration record survived a completed commit")
	}
}

func TestMigrateCommitAbortedLeavesInstancePut(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 5)).(error); err != nil {
		t.Fatalf("begin: %v", err)
	}

	if err, _ := f.applyMigrateCommit(&goblinv1.MigrateCommit{
		InstanceUuid:    migInstA,
		CheckpointEpoch: 5,
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED,
	}).(error); err != nil {
		t.Fatalf("abort: %v", err)
	}

	if inst := mustInstance(t, f, migInstA); inst.NodeId != "node-1" {
		t.Errorf("aborted migration moved node_id to %q", inst.NodeId)
	}
	if _, still := f.MigrationInFlight(migInstA); still {
		t.Error("migration record survived an aborted commit")
	}
	// After an abort the instance must be migratable again.
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 6)).(error); err != nil {
		t.Fatalf("re-begin after abort: %v", err)
	}
}

// A late commit from a superseded attempt must not close the current
// migration on the strength of an older one's outcome.
func TestStaleEpochCommitRejected(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 9)).(error); err != nil {
		t.Fatalf("begin: %v", err)
	}

	err, _ := f.applyMigrateCommit(&goblinv1.MigrateCommit{
		InstanceUuid:    migInstA,
		CheckpointEpoch: 8, // stale
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED,
	}).(error)
	if err == nil {
		t.Fatal("stale-epoch commit was accepted")
	}
	if inst := mustInstance(t, f, migInstA); inst.NodeId != "node-1" {
		t.Errorf("stale commit moved the instance to %q", inst.NodeId)
	}
	if _, still := f.MigrationInFlight(migInstA); !still {
		t.Error("stale commit closed the in-flight migration")
	}
}

// UNSPECIFIED must not resolve in either direction: guessing would
// either strand an instance or move one that never arrived.
func TestUnspecifiedOutcomeRefused(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 1)).(error); err != nil {
		t.Fatalf("begin: %v", err)
	}

	err, _ := f.applyMigrateCommit(&goblinv1.MigrateCommit{
		InstanceUuid: migInstA, CheckpointEpoch: 1,
	}).(error)
	if err == nil {
		t.Fatal("unspecified outcome was accepted")
	}
	if _, still := f.MigrationInFlight(migInstA); !still {
		t.Error("unspecified outcome silently closed the migration")
	}
}

func TestMigrateBeginRequiresRunningAndRights(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")

	noRights := migrateBegin(migInstA, "node-2", 1)
	noRights.Rights = 0
	if err, _ := f.applyMigrateBegin(noRights).(error); err == nil {
		t.Error("migration admitted with an empty rights bitmap")
	}

	sameNode := migrateBegin(migInstA, "node-1", 1)
	if err, _ := f.applyMigrateBegin(sameNode).(error); err == nil {
		t.Error("migration admitted to the node the instance already runs on")
	}

	// An instance that never reached RUNNING cannot be checkpointed.
	if err, _ := f.applyAdmit(&goblinv1.ApplyAdmit{
		SpecUuid: migSpec, InstanceUuid: migInstB, NodeId: "node-1",
	}).(error); err != nil {
		t.Fatalf("admit B: %v", err)
	}
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstB, "node-2", 1)).(error); err == nil {
		t.Error("migration admitted for an instance still in ADMITTED")
	}
}

// The arbitration is only sound if it survives a restart: a replica
// that caught up from a snapshot must still refuse a concurrent move.
func TestMigrationSurvivesSnapshotRestore(t *testing.T) {
	f := NewFSM(nil)
	runningInstance(t, f, migInstA, "node-1")
	if err, _ := f.applyMigrateBegin(migrateBegin(migInstA, "node-2", 3)).(error); err != nil {
		t.Fatalf("begin: %v", err)
	}

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

	rec, ok := restored.MigrationInFlight(migInstA)
	if !ok {
		t.Fatal("in-flight migration lost across snapshot/restore")
	}
	if rec.GetTargetNodeId() != "node-2" || rec.GetCheckpointEpoch() != 3 {
		t.Errorf("restored record = %q/%d, want node-2/3", rec.GetTargetNodeId(), rec.GetCheckpointEpoch())
	}
	if err, _ := restored.applyMigrateBegin(migrateBegin(migInstA, "node-3", 4)).(error); !errors.Is(err, ErrMigrationInFlight) {
		t.Fatalf("restored FSM permitted a concurrent migration: %v", err)
	}
}
