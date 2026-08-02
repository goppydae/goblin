package supervisor

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

// waitForLoop blocks until name is live in tier, so a test acts once a
// phase has actually started rather than after a guessed sleep.
func waitForLoop(t *testing.T, g *loopGroup, tier int, name string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		for _, n := range g.stragglers(tier) {
			if n == name {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("loop %q never started within %s (live: %v)", name, within, g.stragglers(tier))
}

func TestLoopGroup_JoinWaitsForTheLoop(t *testing.T) {
	g := newLoopGroup()
	release := make(chan struct{})
	g.spawn(tierRun, "probe", func() { <-release })

	if got := g.join(tierRun, 50*time.Millisecond); !reflect.DeepEqual(got, []string{"probe"}) {
		t.Fatalf("join() while probe is running = %v, want [probe]", got)
	}

	close(release)
	if got := g.join(tierRun, 5*time.Second); got != nil {
		t.Errorf("join() after release = %v, want nil", got)
	}
}

// TestLoopGroup_StragglersAreSorted pins the report order: a shutdown
// diagnostic that reorders between runs is not a diagnostic.
func TestLoopGroup_StragglersAreSorted(t *testing.T) {
	g := newLoopGroup()
	release := make(chan struct{})
	defer close(release)

	for _, name := range []string{"zulu", "alpha", "mike"} {
		g.spawn(tierRun, name, func() { <-release })
	}

	got := g.join(tierRun, 50*time.Millisecond)
	want := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("join() = %v, want %v", got, want)
	}
}

// TestLoopGroup_TiersAreIndependent is the unit-level guard on the
// reason tier 0 exists: joining the run tier must not wait for the
// pre-userspace loops, because those have to outlive the local
// teardown.
func TestLoopGroup_TiersAreIndependent(t *testing.T) {
	g := newLoopGroup()
	releasePre := make(chan struct{})
	defer close(releasePre)

	g.spawn(tierPreUserspace, "reaper", func() { <-releasePre })
	g.spawn(tierRun, "finishes", func() {})

	if got := g.join(tierRun, 5*time.Second); got != nil {
		t.Errorf("tierRun join = %v, want nil - the reaper is not its business", got)
	}
	if got := g.join(tierPreUserspace, 50*time.Millisecond); !reflect.DeepEqual(got, []string{"reaper"}) {
		t.Errorf("tierPreUserspace join = %v, want [reaper]", got)
	}
}

// TestRun_DoesNotReturnUntilTrackedLoopsHave is GOBLIN-DIV-038's
// closing artifact. It asserts the PROPERTY - Run outlives its loops -
// rather than the presence of a WaitGroup, so a later lifecycle model
// can satisfy it without reopening the entry.
//
// The probe is spawned through the supervisor's own group before Run
// starts, which is why this needs no test-only hook in production code.
func TestRun_DoesNotReturnUntilTrackedLoopsHave(t *testing.T) {
	if testing.Short() {
		t.Skip("full node boot; skipped in short mode")
	}

	sup := New(Config{
		NodeID:     "join-test",
		ListenAddr: "127.0.0.1:39711",
		RaftDir:    t.TempDir(),
	})

	release := make(chan struct{})
	sup.loops.spawn(tierRun, "test-probe", func() { <-release })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	// Cancel only once Phase 5 is up, so this exercises the ordered
	// teardown rather than an early error return.
	waitForLoop(t, sup.loops, tierRun, "cluster-monitor", 60*time.Second)
	cancel()

	select {
	case err := <-done:
		t.Fatalf("Run() returned while a tracked loop was still running (err=%v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("Run() did not return after its tracked loops finished")
	}
}

// TestRun_ReportsLoopsThatOutliveTheGrace covers the other half: a loop
// that ignores cancellation must not wedge shutdown, and must be named
// rather than merely timed out.
func TestRun_ReportsLoopsThatOutliveTheGrace(t *testing.T) {
	if testing.Short() {
		t.Skip("full node boot; skipped in short mode")
	}

	sup := New(Config{
		NodeID:        "straggler-test",
		ListenAddr:    "127.0.0.1:39712",
		RaftDir:       t.TempDir(),
		ShutdownGrace: 250 * time.Millisecond,
	})

	wedged := make(chan struct{})
	defer close(wedged)
	sup.loops.spawn(tierRun, "wedged-probe", func() { <-wedged })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sup.Run(ctx) }()

	waitForLoop(t, sup.loops, tierRun, "cluster-monitor", 60*time.Second)
	cancel()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run() blocked past its grace on a wedged loop")
	}

	got := sup.loops.stragglers(tierRun)
	if !reflect.DeepEqual(got, []string{"wedged-probe"}) {
		t.Errorf("stragglers after shutdown = %v, want [wedged-probe]", got)
	}
}

// TestRun_NetworkGatePrecedesClusterJoin pins the Phase 3/Phase 4
// reordering. Before this change the gate ran after Serf membership,
// Raft consensus and the scheduler were built, so an expiry aborted a
// node that had already joined gossip and written raft state.
//
// The observable is the raft directory: NewConsensus creates
// raft-log.db and raft-stable.db in it, so an empty directory after a
// gate failure proves cluster join was never reached. Asserting on
// files rather than on timing keeps this deterministic.
func TestRun_NetworkGatePrecedesClusterJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("full node boot; skipped in short mode")
	}

	raftDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sup := New(Config{
		NodeID:             "phase-order-test",
		ListenAddr:         "127.0.0.1:39713",
		RaftDir:            raftDir,
		NetworkGateTimeout: 500 * time.Millisecond,
	})

	if err := sup.Run(ctx); err == nil {
		t.Fatal("Run() should fail when the network gate expires")
	}

	entries, err := os.ReadDir(raftDir)
	if err != nil {
		t.Fatalf("read raft dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("raft state exists after a gate failure: %v - cluster join ran before the gate", names)
	}
}
