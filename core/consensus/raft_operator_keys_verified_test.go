package consensus

import (
	"errors"
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// The property under test (GOBLIN-DIV-044): a stale reader cannot say
// yes. A replica that holds registry state but is not the leader must
// REFUSE the verified read rather than answering from state it may not
// have caught up on - because the answer it would give is key material,
// and a follower that has not applied an OPERATOR_KEY_CHANGE remove
// still resolves the removed key.
//
// This is a unit test and not a cluster test, deliberately. The gate
// asked for a cluster test on the 3-node harness; there is no way to
// reach this read path from that harness today, because no RPC exposes
// the operator key registry. test/cluster drives nodes only through
// QUICRPCClient, and the sole registry consumer behind that surface is
// operatorRegistryGate, which reads the COUNT locally on purpose and
// must keep doing so. Exposing a registry read RPC purely to make the
// test reachable would be building the piece-2 surface this change is
// explicitly not building. So the property is asserted directly against
// a Consensus whose raft engine is a follower.

// followerConsensus builds a Consensus over an in-memory raft that is
// never bootstrapped. With no configuration it can never win an
// election, so it stays a Follower for the life of the test - the same
// state a real replica is in, without needing three processes.
func followerConsensus(t *testing.T) *Consensus {
	t.Helper()

	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID("follower")
	config.HeartbeatTimeout = 50 * time.Millisecond
	config.ElectionTimeout = 50 * time.Millisecond
	config.LeaderLeaseTimeout = 50 * time.Millisecond
	config.CommitTimeout = 10 * time.Millisecond
	config.Logger = hclog.NewNullLogger()

	fsm := NewFSM()
	_, transport := raft.NewInmemTransport("")
	r, err := raft.NewRaft(config, fsm, raft.NewInmemStore(), raft.NewInmemStore(),
		raft.NewInmemSnapshotStore(), transport)
	if err != nil {
		t.Fatalf("NewRaft: %v", err)
	}
	t.Cleanup(func() {
		if serr := r.Shutdown().Error(); serr != nil {
			t.Logf("raft shutdown: %v", serr)
		}
	})

	return &Consensus{raft: r, fsm: fsm, transport: transport, nodeID: "follower"}
}

// TestVerifiedOperatorKeyReadRefusedOnFollower is the gate. The follower
// HOLDS registry state - the assertion is not that it has nothing to
// say, it is that it refuses to say it.
func TestVerifiedOperatorKeyReadRefusedOnFollower(t *testing.T) {
	c := followerConsensus(t)

	root, _ := opKey(t, "root")
	if err, _ := c.fsm.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{root},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if c.IsLeader() {
		t.Fatal("test setup is wrong: the unbootstrapped node became leader")
	}
	// The local read still answers - that is the whole hazard, and the
	// test is worthless if this side is broken instead.
	if local, _ := c.OperatorKeysLocal(); len(local) != 1 {
		t.Fatalf("local read returned %d keys, want 1; the follower does not hold registry state", len(local))
	}

	keys, serial, err := c.OperatorKeysVerified()
	if err == nil {
		t.Fatal("OperatorKeysVerified answered on a follower; a replica that has not applied a " +
			"pending OPERATOR_KEY_CHANGE would authorize from a revoked key")
	}
	if !errors.Is(err, raft.ErrNotLeader) {
		t.Errorf("refusal did not wrap raft.ErrNotLeader: %v", err)
	}
	if keys != nil || serial != 0 {
		t.Errorf("a refused read returned data: %d key(s), serial %d", len(keys), serial)
	}
}

// TestVerifiedOperatorKeyReadAnswersOnLeader is the matching positive.
// Without it, an accessor that returned an error unconditionally would
// still pass the test above.
func TestVerifiedOperatorKeyReadAnswersOnLeader(t *testing.T) {
	c := followerConsensus(t)

	addr := c.transport.(*raft.InmemTransport).LocalAddr()
	if err := c.Bootstrap([]raft.Server{{ID: raft.ServerID("follower"), Address: addr}}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for !c.IsLeader() {
		if time.Now().After(deadline) {
			t.Fatalf("single-node raft never became leader (state %s)", c.raft.State())
		}
		time.Sleep(10 * time.Millisecond)
	}

	root, _ := opKey(t, "root")
	if err, _ := c.fsm.applyOperatorKeySeed(&goblinv1.OperatorKeySeed{
		Keys: []*goblinv1.OperatorKey{root},
	}).(error); err != nil {
		t.Fatalf("seed: %v", err)
	}

	keys, serial, err := c.OperatorKeysVerified()
	if err != nil {
		t.Fatalf("OperatorKeysVerified refused on the leader: %v", err)
	}
	if len(keys) != 1 || keys[0].GetKeyId() != root.GetKeyId() {
		t.Fatalf("leader returned %d key(s), want the seeded root", len(keys))
	}
	if serial == 0 {
		t.Error("leader returned serial 0 for a seeded registry")
	}
}
