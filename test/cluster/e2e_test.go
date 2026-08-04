// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build cluster

package main_test

import (
	"testing"
	"time"

	goblintransport "github.com/goppydae/goblin/core/transport"
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

	// --- Scenario 1a: the single-port claim, proven mechanically
	// (GOBLIN-DIV-023). Every registry ALPN answers on the leader's ONE
	// advertised address - gossip, raft, RPC, and kernel are all planes
	// of the same listener.
	for _, alpn := range []string{
		goblintransport.ALPNGoblinRPC,
		goblintransport.ALPNSerfQUIC,
		goblintransport.ALPNRaftQUIC,
		goblintransport.ALPNGapiQUIC,
	} {
		conn, err := dialALPNAddr(leader.listenAddr, alpn)
		if err != nil {
			t.Fatalf("single-port proof: ALPN %s refused on %s: %v", alpn, leader.listenAddr, err)
		}
		if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != alpn {
			t.Fatalf("single-port proof: negotiated %q, want %q", got, alpn)
		}
		_ = conn.CloseWithError(0, "probe done")
	}
	t.Logf("single-port proof: all four ALPNs served on %s", leader.listenAddr)

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
	req := &goblinv1.RegisterGlobalAgentRequest{Spec: spec}
	var resp goblinv1.RegisterGlobalAgentResponse
	// A freshly elected leader's operator-key seeder (GOBLIN-DIV-015
	// piece 1) commits the configured key to Raft on its own poll tick
	// after it notices leadership; that can trail a short beat behind
	// waitLeader's observation of the same leadership over RPC. Retry
	// this one refusal - PERMISSION_DENIED, the code the fail-closed
	// gate maps to - rather than widen waitLeader's contract with a
	// registry-read RPC this piece deliberately does not add. Branching
	// on the code is the contract in rpc.proto; the message is for
	// humans. Any other error, or the same one past the deadline,
	// fails immediately.
	//
	// This depends on PERMISSION_DENIED meaning exactly one thing today:
	// the registry is empty, which is transient at first seed. Other
	// authorization refusals - an ungrantable verb, insufficient rights -
	// are still classified INTERNAL and so are not retried. Whoever maps
	// a second sentinel onto PERMISSION_DENIED must narrow this check at
	// the same time, or a permanent refusal will be retried for ten
	// seconds before failing.
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := cl.Call("SchedulerRPC.RegisterGlobalAgent", req, &resp)
		if err == nil {
			break
		}
		if !supervisor.IsPermissionDenied(err) || time.Now().After(deadline) {
			c.t.Fatalf("register %s: %v", spec.Name, err)
		}
		time.Sleep(100 * time.Millisecond)
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
	req := &goblinv1.ScaleAgentRequest{AgentId: specID, Replicas: replicas}
	var resp goblinv1.ScaleAgentResponse
	if err := cl.Call("SchedulerRPC.ScaleAgent", req, &resp); err != nil {
		c.t.Fatalf("scale %s to %d: %v", specID, replicas, err)
	}
}
