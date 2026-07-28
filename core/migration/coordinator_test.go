package migration_test

import (
	"context"
	"errors"
	"testing"

	"github.com/goppydae/goblin/core/migration"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Recording fakes. The point of these tests is the ORDER and the
// rollback, not the transport: a migration that does the right steps in
// the wrong order, or skips the undo, is the failure mode that matters.

type fakeProposer struct {
	begins   []*goblinv1.MigrateBegin
	commits  []*goblinv1.MigrateCommit
	beginErr error
	commitEr error
}

func (f *fakeProposer) ProposeMigrateBegin(_ context.Context, mb *goblinv1.MigrateBegin) error {
	f.begins = append(f.begins, mb)
	return f.beginErr
}

func (f *fakeProposer) ProposeMigrateCommit(_ context.Context, mc *goblinv1.MigrateCommit) error {
	f.commits = append(f.commits, mc)
	return f.commitEr
}

type call struct {
	op     string
	nodeID string
}

type fakeNodes struct {
	calls      []call
	ckptErr    error
	restoreErr map[string]error // keyed by node id
}

func (f *fakeNodes) Checkpoint(_ context.Context, nodeID, _ string, _ []byte, _ uint64) error {
	f.calls = append(f.calls, call{op: "checkpoint", nodeID: nodeID})
	return f.ckptErr
}

func (f *fakeNodes) Restore(_ context.Context, nodeID, _ string, _ []byte, _ uint64, _ *goblinv1.AgentSpec) error {
	f.calls = append(f.calls, call{op: "restore", nodeID: nodeID})
	return f.restoreErr[nodeID]
}

type fakePuller struct {
	called bool
	err    error
}

func (f *fakePuller) Pull(_ context.Context, _, _ string, _ []byte, _ uint64, _ []byte) error {
	f.called = true
	return f.err
}

var coordUUID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func request() migration.Request {
	return migration.Request{
		InstanceID:   "inst-1",
		InstanceUUID: coordUUID,
		SourceNode:   "node-1",
		TargetNode:   "node-2",
		Epoch:        4,
		Rights:       1 << 13,
		Spec:         &goblinv1.AgentSpec{Type: "worker"},
	}
}

func TestMigrateHappyPathOrdersStepsCorrectly(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{restoreErr: map[string]error{}}
	puller := &fakePuller{}

	err := migration.NewCoordinator(raft, nodes, puller, nil).
		Migrate(context.Background(), request())
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if len(raft.begins) != 1 {
		t.Fatalf("begins = %d, want 1", len(raft.begins))
	}
	if !puller.called {
		t.Error("image was never pulled")
	}
	// Dump on the source must precede restore on the target: restoring
	// first would mean two live copies of one instance.
	if len(nodes.calls) != 2 ||
		nodes.calls[0] != (call{op: "checkpoint", nodeID: "node-1"}) ||
		nodes.calls[1] != (call{op: "restore", nodeID: "node-2"}) {
		t.Fatalf("step order = %+v, want checkpoint@node-1 then restore@node-2", nodes.calls)
	}
	if len(raft.commits) != 1 ||
		raft.commits[0].GetOutcome() != goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED {
		t.Fatalf("commits = %+v, want one COMPLETED", raft.commits)
	}
}

// The instance must come back on its source, from the same image, and
// the migration must be recorded as aborted.
func TestRestoreFailureRollsBackToSource(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{restoreErr: map[string]error{"node-2": errors.New("criu restore failed")}}

	err := migration.NewCoordinator(raft, nodes, &fakePuller{}, nil).
		Migrate(context.Background(), request())
	if !errors.Is(err, migration.ErrRolledBack) {
		t.Fatalf("want ErrRolledBack, got %v", err)
	}

	// checkpoint@1, failed restore@2, rollback restore@1
	if len(nodes.calls) != 3 || nodes.calls[2] != (call{op: "restore", nodeID: "node-1"}) {
		t.Fatalf("calls = %+v, want a rollback restore on node-1", nodes.calls)
	}
	if len(raft.commits) != 1 ||
		raft.commits[0].GetOutcome() != goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED {
		t.Fatalf("commits = %+v, want one ABORTED", raft.commits)
	}
}

func TestPullFailureRollsBackToSource(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{restoreErr: map[string]error{}}
	puller := &fakePuller{err: errors.New("connection reset")}

	err := migration.NewCoordinator(raft, nodes, puller, nil).
		Migrate(context.Background(), request())
	if !errors.Is(err, migration.ErrRolledBack) {
		t.Fatalf("want ErrRolledBack, got %v", err)
	}
	// The target must never have been asked to restore.
	for _, c := range nodes.calls {
		if c.op == "restore" && c.nodeID == "node-2" {
			t.Fatal("restore attempted on the target after the pull failed")
		}
	}
}

// Failure plus a failed undo is a different situation from a clean
// rollback: nothing is running, and a caller that retries blindly makes
// it worse.
func TestFailedRollbackIsDistinctFromRolledBack(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{restoreErr: map[string]error{
		"node-2": errors.New("restore failed"),
		"node-1": errors.New("rollback failed too"),
	}}

	err := migration.NewCoordinator(raft, nodes, &fakePuller{}, nil).
		Migrate(context.Background(), request())
	if !errors.Is(err, migration.ErrStranded) {
		t.Fatalf("want ErrStranded, got %v", err)
	}
	if errors.Is(err, migration.ErrRolledBack) {
		t.Error("a stranded instance must not report as rolled back")
	}
}

// A dump that never happened leaves the source running, so there is
// nothing to restore - attempting one would be wrong.
func TestCheckpointFailureDoesNotRestore(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{ckptErr: errors.New("not checkpointable"), restoreErr: map[string]error{}}

	err := migration.NewCoordinator(raft, nodes, &fakePuller{}, nil).
		Migrate(context.Background(), request())
	if err == nil {
		t.Fatal("checkpoint failure was not reported")
	}
	for _, c := range nodes.calls {
		if c.op == "restore" {
			t.Fatalf("restore attempted after a failed checkpoint: %+v", nodes.calls)
		}
	}
	if len(raft.commits) != 1 ||
		raft.commits[0].GetOutcome() != goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED {
		t.Fatalf("commits = %+v, want one ABORTED", raft.commits)
	}
}

// If the intent is refused, nothing may touch the process at all.
func TestRefusedIntentTouchesNothing(t *testing.T) {
	raft := &fakeProposer{beginErr: errors.New("migration already in flight")}
	nodes := &fakeNodes{restoreErr: map[string]error{}}
	puller := &fakePuller{}

	if err := migration.NewCoordinator(raft, nodes, puller, nil).
		Migrate(context.Background(), request()); err == nil {
		t.Fatal("refused intent was not reported")
	}
	if len(nodes.calls) != 0 {
		t.Errorf("node calls made after a refused begin: %+v", nodes.calls)
	}
	if puller.called {
		t.Error("image pulled after a refused begin")
	}
	if len(raft.commits) != 0 {
		t.Errorf("commit written for a migration that never began: %+v", raft.commits)
	}
}

// A completed migration whose commit fails must NOT be rolled back:
// the process is healthy on the target, and killing it to satisfy
// bookkeeping would turn a stale record into an outage.
func TestCommitFailureDoesNotKillHealthyInstance(t *testing.T) {
	raft := &fakeProposer{commitEr: errors.New("no quorum")}
	nodes := &fakeNodes{restoreErr: map[string]error{}}

	err := migration.NewCoordinator(raft, nodes, &fakePuller{}, nil).
		Migrate(context.Background(), request())
	if err == nil {
		t.Fatal("commit failure was not reported")
	}
	if errors.Is(err, migration.ErrRolledBack) || errors.Is(err, migration.ErrStranded) {
		t.Errorf("commit failure was treated as a rollback: %v", err)
	}
	// Exactly the two expected node calls: no rollback restore.
	if len(nodes.calls) != 2 {
		t.Fatalf("calls = %+v, want checkpoint then restore only", nodes.calls)
	}
}

func TestMigrateRejectsBadRequests(t *testing.T) {
	c := migration.NewCoordinator(&fakeProposer{}, &fakeNodes{restoreErr: map[string]error{}}, &fakePuller{}, nil)

	bad := request()
	bad.InstanceUUID = []byte{1, 2, 3}
	if err := c.Migrate(context.Background(), bad); !errors.Is(err, migration.ErrBadUUID) {
		t.Errorf("want ErrBadUUID, got %v", err)
	}

	same := request()
	same.TargetNode = same.SourceNode
	if err := c.Migrate(context.Background(), same); err == nil {
		t.Error("migration to the instance's own node was accepted")
	}
}
