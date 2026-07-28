package migration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/migration"
	goblinv1 "github.com/goppydae/goblin/proto"
)

type fakeApplier struct {
	data    []byte
	timeout time.Duration
	resp    interface{}
	err     error
}

func (f *fakeApplier) ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error) {
	f.data = data
	f.timeout = timeout
	return f.resp, f.err
}

func decodeEntry(t *testing.T, data []byte) *goblinv1.LogEntry {
	t.Helper()
	var e goblinv1.LogEntry
	if err := proto.Unmarshal(data, &e); err != nil {
		t.Fatalf("decoding proposed entry: %v", err)
	}
	return &e
}

func TestProposeMigrateBeginEncodesTheOneof(t *testing.T) {
	app := &fakeApplier{}
	p := migration.NewRaftProposer(app, 0)

	err := p.ProposeMigrateBegin(context.Background(), &goblinv1.MigrateBegin{
		InstanceUuid: rpcUUID, TargetNodeId: "node-2", CheckpointEpoch: 5,
	})
	if err != nil {
		t.Fatalf("ProposeMigrateBegin: %v", err)
	}

	entry := decodeEntry(t, app.data)
	if entry.GetType() != goblinv1.CommandType_COMMAND_TYPE_MIGRATE_BEGIN {
		t.Errorf("type = %v, want MIGRATE_BEGIN", entry.GetType())
	}
	// The payload must land in the oneof, not merely alongside it: the
	// FSM reads cmd.GetMigrateBegin() and would see nil otherwise.
	if entry.GetMigrateBegin().GetTargetNodeId() != "node-2" {
		t.Error("MigrateBegin payload did not reach the oneof")
	}
	if app.timeout <= 0 {
		t.Error("a zero timeout was passed through; a migration would hang on an unreachable quorum")
	}
}

func TestProposeMigrateCommitEncodesTheOneof(t *testing.T) {
	app := &fakeApplier{}
	p := migration.NewRaftProposer(app, time.Second)

	if err := p.ProposeMigrateCommit(context.Background(), &goblinv1.MigrateCommit{
		InstanceUuid:    rpcUUID,
		CheckpointEpoch: 5,
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED,
	}); err != nil {
		t.Fatalf("ProposeMigrateCommit: %v", err)
	}

	entry := decodeEntry(t, app.data)
	if entry.GetType() != goblinv1.CommandType_COMMAND_TYPE_MIGRATE_COMMIT {
		t.Errorf("type = %v, want MIGRATE_COMMIT", entry.GetType())
	}
	if entry.GetMigrateCommit().GetOutcome() != goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED {
		t.Error("MigrateCommit payload did not reach the oneof")
	}
}

// The FSM reports refusals - concurrent migration, stale epoch, missing
// rights - through the Apply RESPONSE. A proposer that only inspected
// the transport error would read every rejection as a success and let
// the coordinator proceed to dump a process it was told not to move.
func TestFSMRejectionSurfacesAsError(t *testing.T) {
	rejected := errors.New("migration already in flight for this instance")
	app := &fakeApplier{resp: rejected}
	p := migration.NewRaftProposer(app, time.Second)

	err := p.ProposeMigrateBegin(context.Background(), &goblinv1.MigrateBegin{
		InstanceUuid: rpcUUID, TargetNodeId: "node-2",
	})
	if err == nil {
		t.Fatal("an FSM rejection was reported as success")
	}
	if !errors.Is(err, rejected) {
		t.Errorf("rejection did not propagate: %v", err)
	}
}

func TestTransportErrorSurfaces(t *testing.T) {
	boom := errors.New("no leader")
	app := &fakeApplier{err: boom}
	p := migration.NewRaftProposer(app, time.Second)

	if err := p.ProposeMigrateBegin(context.Background(), &goblinv1.MigrateBegin{
		InstanceUuid: rpcUUID, TargetNodeId: "node-2",
	}); !errors.Is(err, boom) {
		t.Fatalf("want the transport error, got %v", err)
	}
}

// A nil response is the FSM's success signal and must not be mistaken
// for a rejection.
func TestNilResponseIsSuccess(t *testing.T) {
	p := migration.NewRaftProposer(&fakeApplier{resp: nil}, time.Second)
	if err := p.ProposeMigrateCommit(context.Background(), &goblinv1.MigrateCommit{
		InstanceUuid: rpcUUID, Outcome: goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED,
	}); err != nil {
		t.Fatalf("nil response treated as failure: %v", err)
	}
}
