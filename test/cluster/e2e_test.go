//go:build cluster

package main_test

import (
	"testing"
	"time"

	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Latency targets, operator-set (phase2-completion-plan decision 3).
const (
	placementTarget = 5 * time.Second
	failoverTarget  = 30 * time.Second
)

// TestClusterEndToEnd is the phase 2b exit: a 3-node cluster schedules,
// reconciles, and fails over real agent instances end-to-end, inside the
// operator's latency targets.
func TestClusterEndToEnd(t *testing.T) {
	c := startCluster(t, 3)
	leader := c.waitLeader(c.anyNode(), 3, 60*time.Second)
	t.Logf("cluster up, leader=%s", leader.id)

	// --- Scenario 1: placement. replicas=3 lands running on 3 distinct
	// nodes within the placement target.
	spec := &goblinv1.AgentSpec{Name: "sleeper-spec", Type: "sleeper", Replicas: 3, Strategy: "spread"}
	c.register(leader, spec)
	insts, elapsed := c.waitInstances(leader, "sleeper-spec", 3, placementTarget)
	nodesSeen := map[string]bool{}
	for _, in := range insts {
		nodesSeen[in.NodeId] = true
	}
	if len(nodesSeen) != 3 {
		t.Fatalf("placement: 3 instances should spread across 3 nodes, got %v", describeInstances(insts))
	}
	t.Logf("placement: 3/3 running on %d nodes in %s (target %s)", len(nodesSeen), elapsed, placementTarget)

	// --- Scenario 2a: the signal path (phase 2c). SIGTERM one instance:
	// the leader issues a capability token, verifies it, authorizes the
	// request at the Raft FSM, and the hosting node delivers through the
	// start-epoch + pidfd guard. The sleeper exits on SIGTERM, its node
	// reports the failure, and the reconciler replaces it.
	target := insts[0]
	targetID := uuidStr(target.InstanceUuid)
	c.signal(leader, targetID, 15)
	deadline := time.Now().Add(failoverTarget)
	for {
		cur, err := c.instances(leader, "sleeper-spec")
		if err == nil {
			replaced := true
			running := 0
			for _, in := range cur {
				if uuidStr(in.InstanceUuid) == targetID &&
					in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
					replaced = false
				}
				if in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
					running++
				}
			}
			if replaced && running == 3 {
				t.Logf("signal: %s terminated and replaced, 3/3 running again", targetID)
				break
			}
		}
		if time.Now().After(deadline) {
			cur, _ := c.instances(leader, "sleeper-spec")
			t.Fatalf("signaled instance %s not replaced within %s: %v", targetID, failoverTarget, describeInstances(cur))
		}
		time.Sleep(250 * time.Millisecond)
	}

	// A non-grantable signal must be refused at authorization, before
	// any process is touched.
	if err := c.trySignal(leader, uuidStr(insts[1].InstanceUuid), 11); err == nil {
		t.Fatal("signal: SIGSEGV was authorized - the rights bitmap must refuse it")
	} else {
		t.Logf("signal: SIGSEGV correctly refused (%v)", err)
	}

	// --- Scenario 2: scale down and back up (full cluster).
	c.scale(leader, "sleeper-spec", 1)
	_, elapsed = c.waitInstances(leader, "sleeper-spec", 1, failoverTarget)
	t.Logf("scale 3->1 converged in %s", elapsed)
	c.scale(leader, "sleeper-spec", 3)
	_, elapsed = c.waitInstances(leader, "sleeper-spec", 3, failoverTarget)
	t.Logf("scale 1->3 converged in %s", elapsed)

	// --- Scenario 3: leader kill. Two birds: a new leader must emerge
	// (quorum 2/3 survives), and the dead node's instances must be
	// re-placed on survivors - all within the failover target.
	killed := leader
	killStart := time.Now()
	c.killNode(killed)
	t.Logf("killed leader %s", killed.id)

	survivor := c.anyNode()
	newLeader := c.waitLeader(survivor, 2, failoverTarget)
	t.Logf("new leader %s in %s", newLeader.id, time.Since(killStart))

	// The killed node hosted one of the three instances; heartbeat
	// staleness plus reconcile must re-place it on a survivor.
	deadline = time.Now().Add(failoverTarget)
	for {
		insts, err := c.instances(newLeader, "sleeper-spec")
		if err == nil {
			running := 0
			onDead := 0
			for _, in := range insts {
				if in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
					running++
					if in.NodeId == killed.id {
						onDead++
					}
				}
			}
			if running == 3 && onDead == 0 {
				t.Logf("failover: 3/3 running on survivors in %s (target %s)", time.Since(killStart), failoverTarget)
				break
			}
		}
		if time.Now().After(deadline) {
			insts, _ := c.instances(newLeader, "sleeper-spec")
			t.Fatalf("failover: instances not re-placed within %s: %v", failoverTarget, describeInstances(insts))
		}
		time.Sleep(250 * time.Millisecond)
	}

	// --- Scenario 4: the new leader schedules fresh work (reconciliation
	// resumed, not just survived).
	spec2 := &goblinv1.AgentSpec{Name: "post-failover-spec", Type: "sleeper", Replicas: 1, Strategy: "spread"}
	c.register(newLeader, spec2)
	_, elapsed = c.waitInstances(newLeader, "post-failover-spec", 1, placementTarget)
	t.Logf("post-failover placement in %s (target %s)", elapsed, placementTarget)
}

// anyNode returns an arbitrary live node.
func (c *testCluster) anyNode() *clusterNode {
	c.t.Helper()
	for _, n := range c.nodes {
		return n
	}
	c.t.Fatal("no live nodes")
	return nil
}

// register submits a spec through the given node (writes need the leader).
func (c *testCluster) register(node *clusterNode, spec *goblinv1.AgentSpec) {
	c.t.Helper()
	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close register client: %v", cerr)
		}
	}()
	var resp string
	if err := cl.Call("SchedulerRPC.RegisterGlobalAgent", spec, &resp); err != nil {
		c.t.Fatalf("register %s: %v", spec.Name, err)
	}
}

// scale sets a spec's replica count through the given node.
func (c *testCluster) scale(node *clusterNode, specID string, replicas int32) {
	c.t.Helper()
	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close scale client: %v", cerr)
		}
	}()
	req := supervisor.ScaleAgentRequest{AgentID: specID, Replicas: replicas}
	var resp string
	if err := cl.Call("SchedulerRPC.ScaleAgent", &req, &resp); err != nil {
		c.t.Fatalf("scale %s to %d: %v", specID, replicas, err)
	}
}
