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
	// notReady makes the destination refuse the pre-flight. The zero
	// value is READY, so every existing test keeps exercising the path
	// it was written for rather than silently short-circuiting at a new
	// first step.
	notReady string
	readyErr error
}

func (f *fakeNodes) Ready(_ context.Context, nodeID, _ string) (string, bool, error) {
	f.calls = append(f.calls, call{op: "ready", nodeID: nodeID})
	if f.readyErr != nil {
		return "", false, f.readyErr
	}
	if f.notReady != "" {
		return f.notReady, false, nil
	}
	return "", true, nil
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

// mutating drops the read-only readiness probe. Several tests mean
// "nothing was DONE to the instance", and asking a node whether it can
// accept one is not doing anything to it - so they assert over the
// mutating calls rather than over an exact transcript, which would
// break again the next time a question is added ahead of the work.
func mutating(calls []call) []call {
	out := make([]call, 0, len(calls))
	for _, c := range calls {
		if c.op != "ready" {
			out = append(out, c)
		}
	}
	return out
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
	// The destination is asked whether it can accept BEFORE the source
	// is dumped (GOBLIN-DIV-048), and dump on the source precedes
	// restore on the target: restoring first would mean two live copies
	// of one instance. The order is asserted as a whole rather than
	// per-step, because every defect this test exists to catch is an
	// ordering defect.
	want := []call{
		{op: "ready", nodeID: "node-2"},
		{op: "checkpoint", nodeID: "node-1"},
		{op: "restore", nodeID: "node-2"},
	}
	if len(nodes.calls) != len(want) {
		t.Fatalf("step order = %+v, want %+v", nodes.calls, want)
	}
	for i := range want {
		if nodes.calls[i] != want[i] {
			t.Fatalf("step order = %+v, want %+v", nodes.calls, want)
		}
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

	// ready@2, checkpoint@1, failed restore@2, rollback restore@1. What
	// this test is about is the LAST call being the undo, so it asserts
	// that rather than a fixed index - an index breaks whenever a step
	// is added ahead of it, which says nothing about rollback.
	if len(nodes.calls) == 0 ||
		nodes.calls[len(nodes.calls)-1] != (call{op: "restore", nodeID: "node-1"}) {
		t.Fatalf("calls = %+v, want the last call to be a rollback restore on node-1", nodes.calls)
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
	if got := mutating(nodes.calls); len(got) != 0 {
		t.Errorf("the instance was touched after a refused begin: %+v", got)
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
	// The claim is that no ROLLBACK happened - a healthy instance was
	// not killed to satisfy bookkeeping. Asserting that directly beats
	// counting calls, which conflates "no rollback" with "no other step
	// was ever added".
	for _, c := range mutating(nodes.calls) {
		if c == (call{op: "restore", nodeID: "node-1"}) {
			t.Fatalf("a healthy instance was rolled back after a commit failure: %+v", nodes.calls)
		}
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

// GOBLIN-DIV-048: a destination that cannot accept the image must be
// refused BEFORE the source is checkpointed.
//
// The assertion that matters is not the error - it is that no
// checkpoint call was ever made. The old failure returned an error too;
// it just returned it after the source process was already dead and had
// to be resurrected from its own image.
func TestMigrateRefusesUnreadyTargetBeforeTouchingTheSource(t *testing.T) {
	raft := &fakeProposer{}
	nodes := &fakeNodes{
		restoreErr: map[string]error{},
		notReady:   "operator key registry has not been applied on this node",
	}
	puller := &fakePuller{}
	c := migration.NewCoordinator(raft, nodes, puller, nil)

	err := c.Migrate(context.Background(), request())
	if err == nil {
		t.Fatal("migration to an unready destination succeeded")
	}
	if !errors.Is(err, migration.ErrTargetNotReady) {
		t.Fatalf("error = %v, want it to wrap ErrTargetNotReady", err)
	}

	for _, c := range nodes.calls {
		if c.op == "checkpoint" {
			t.Fatal("the source was checkpointed despite the destination being unready; " +
				"this is GOBLIN-DIV-048 - the instance is killed and then found to have " +
				"nowhere to go")
		}
	}
	if puller.called {
		t.Error("the image was pulled despite the destination being unready")
	}
	// No intent either: a refusal here must not leave a migration
	// in-flight for something else to reconcile.
	if len(raft.begins) != 0 {
		t.Errorf("recorded %d migration intents for a refused migration, want 0", len(raft.begins))
	}
}

// An unreachable destination is a different failure from an unready
// one, and must not be reported as a refusal - the operator would go
// looking at the wrong node.
func TestMigrateSurfacesAnUnreachableTargetDistinctly(t *testing.T) {
	raft := &fakeProposer{}
	boom := errors.New("dial: connection refused")
	nodes := &fakeNodes{restoreErr: map[string]error{}, readyErr: boom}
	c := migration.NewCoordinator(raft, nodes, &fakePuller{}, nil)

	err := c.Migrate(context.Background(), request())
	if err == nil {
		t.Fatal("migration with an unreachable destination succeeded")
	}
	if errors.Is(err, migration.ErrTargetNotReady) {
		t.Error("an unreachable destination was reported as an unready one")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap the transport failure", err)
	}
}
