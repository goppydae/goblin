//go:build cluster && criu

// Two-node live migration (GOBLIN-DIV-031 item 3).
//
// Double-tagged on purpose. `cluster` brings the multi-node harness;
// `criu` marks it as needing CAP_CHECKPOINT_RESTORE, which no
// unprivileged process holds, so this runs inside the NixOS VM check
// and never in the ordinary suite.
//
// This is the test that decides whether migration works. Everything
// else about the orchestration is proven against fakes or inside a
// single guest; here a real process is moved between two real nodes
// over a real network, through the same goblinctl-facing RPC an
// operator would call.
package main_test

import (
	"testing"
	"time"

	gapicheckpoint "github.com/goppydae/gapi/core/checkpoint"
	goblinv1 "github.com/goppydae/goblin/proto"
)

const migrationTarget = 90 * time.Second

// migrateInstance drives the operator-facing verb through the leader.
func (c *testCluster) migrateInstance(node *clusterNode, instanceID, toNode string) (*goblinv1.MigrateInstanceResponse, error) {
	c.t.Helper()
	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close migrate client: %v", cerr)
		}
	}()
	req := &goblinv1.MigrateInstanceRequest{InstanceId: instanceID, ToNode: toNode}
	var resp goblinv1.MigrateInstanceResponse
	err := cl.Call("SchedulerRPC.MigrateInstance", req, &resp)
	return &resp, err
}

// findInstance returns the current record for one instance UUID.
func (c *testCluster) findInstance(node *clusterNode, specID, instanceID string) (*goblinv1.AgentInstance, bool) {
	c.t.Helper()
	insts, err := c.instances(node, specID)
	if err != nil {
		return nil, false
	}
	for _, in := range insts {
		if uuidStr(in.InstanceUuid) == instanceID {
			return in, true
		}
	}
	return nil, false
}

func TestTwoNodeLiveMigration(t *testing.T) {
	// Fail rather than skip: under the criu tag, a quiet pass would be
	// the GAPI-DIV-028 failure mode this whole suite exists to avoid.
	if err := gapicheckpoint.Available(); err != nil {
		t.Fatalf("checkpointing unavailable in a build tagged for it: %v", err)
	}

	c := startCluster(t, 2)
	leader := c.waitLeader(c.anyNode(), 2, 60*time.Second)
	t.Logf("cluster up, leader=%s", leader.id)

	// One instance, so there is exactly one thing to move.
	spec := &goblinv1.AgentSpec{Name: "sleeper-spec", Type: "sleeper", Replicas: 1}
	c.register(leader, spec)
	insts, elapsed := c.waitInstances(leader, "sleeper-spec", 1, placementTarget)
	t.Logf("instance running in %s", elapsed)

	target := insts[0]
	instanceID := uuidStr(target.InstanceUuid)
	sourceNode := target.NodeId

	// The destination is whichever node is not hosting it.
	var destNode string
	for _, n := range c.nodes {
		if n.id != sourceNode {
			destNode = n.id
			break
		}
	}
	if destNode == "" {
		t.Fatalf("no second node to migrate to; instance is on %s", sourceNode)
	}
	t.Logf("migrating instance %s: %s -> %s", instanceID, sourceNode, destNode)

	resp, err := c.migrateInstance(leader, instanceID, destNode)
	if err != nil {
		t.Fatalf("migrate %s to %s: %v", instanceID, destNode, err)
	}
	t.Logf("migrate returned: instance %s from %s to %s", resp.GetInstanceId(), resp.GetFromNode(), resp.GetToNode())

	// --- the assertions that matter -----------------------------------

	deadline := time.Now().Add(migrationTarget)
	var moved *goblinv1.AgentInstance
	for time.Now().Before(deadline) {
		in, ok := c.findInstance(leader, "sleeper-spec", instanceID)
		if ok && in.NodeId == destNode {
			moved = in
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if moved == nil {
		cur, _ := c.instances(leader, "sleeper-spec")
		t.Fatalf("instance %s did not land on %s within %s: %s",
			instanceID, destNode, migrationTarget, describeInstances(cur))
	}

	// The identity is the whole point: the instance UUID survives the
	// move. A new UUID would mean the process was replaced, not moved.
	if got := uuidStr(moved.InstanceUuid); got != instanceID {
		t.Fatalf("instance UUID changed across migration: %s -> %s", instanceID, got)
	}

	// And it is still RUNNING, not restarted into some other state.
	if moved.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("instance is %v after migration, want RUNNING", moved.State)
	}

	// The instance count must not have grown: a migration that leaves a
	// copy behind is a split brain, not a move.
	cur, err := c.instances(leader, "sleeper-spec")
	if err != nil {
		t.Fatalf("listing instances after migration: %v", err)
	}
	running := 0
	for _, in := range cur {
		if in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
			running++
		}
	}
	if running != 1 {
		t.Errorf("%d instances running after migrating one, want 1: %s", running, describeInstances(cur))
	}

	t.Logf("migration complete: %s runs on %s under its original UUID", instanceID, destNode)
}

// A migration to the node an instance already occupies must be refused
// before anything is dumped. Getting this wrong would stop a healthy
// process to move it nowhere.
func TestMigrateToSameNodeRefused(t *testing.T) {
	if err := gapicheckpoint.Available(); err != nil {
		t.Fatalf("checkpointing unavailable in a build tagged for it: %v", err)
	}

	c := startCluster(t, 2)
	leader := c.waitLeader(c.anyNode(), 2, 60*time.Second)

	spec := &goblinv1.AgentSpec{Name: "sleeper-same", Type: "sleeper", Replicas: 1}
	c.register(leader, spec)
	insts, _ := c.waitInstances(leader, "sleeper-same", 1, placementTarget)

	target := insts[0]
	instanceID := uuidStr(target.InstanceUuid)

	if _, err := c.migrateInstance(leader, instanceID, target.NodeId); err == nil {
		t.Fatal("migration to the instance's own node was accepted")
	}

	// The instance must be untouched: still running, still there.
	in, ok := c.findInstance(leader, "sleeper-same", instanceID)
	if !ok {
		t.Fatal("instance disappeared after a refused migration")
	}
	if in.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("refused migration left the instance %v, want RUNNING", in.State)
	}
}
