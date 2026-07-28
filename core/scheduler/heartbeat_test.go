package scheduler

import (
	"context"
	"testing"
	"time"

	gapiclock "github.com/goppydae/gapi/core/clock"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
)

func newHeartbeatScheduler(t *testing.T) (*Scheduler, *gapiclock.MockClock) {
	t.Helper()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
		{Name: "node-2", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	s := NewScheduler(NewMockStore(), NewMockCluster(nodes), nil, nil, nil)
	clk := &gapiclock.MockClock{CurrentTime: time.Unix(1000, 0)}
	s.SetClock(clk)
	return s, clk
}

func registerRunningInstance(t *testing.T, s *Scheduler, specID, instanceID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterAgent(ctx, &goblinv1.AgentSpec{Id: specID, Replicas: 1}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if err := s.SaveInstance(ctx, &goblinv1.AgentInstance{
		InstanceId: instanceID, SpecId: specID, NodeId: nodeID, State: "running",
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
}

func instanceStates(t *testing.T, s *Scheduler, specID string) map[string]string {
	t.Helper()
	instances, err := s.ListInstances(context.Background(), specID)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	out := map[string]string{}
	for _, inst := range instances {
		out[inst.InstanceId] = inst.State
	}
	return out
}

// A fresh heartbeat keeps a running instance alive across reconciles.
func TestReconcile_FreshHeartbeatKeepsInstance(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	registerRunningInstance(t, s, "spec-a", "inst-1", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil { // leader grace baseline
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat("inst-1", "node-1", "running", clk.Now())
	clk.Advance(HeartbeatCadence)
	s.ObserveHeartbeat("inst-1", "node-1", "running", clk.Now())

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states := instanceStates(t, s, "spec-a")
	if states["inst-1"] != "running" {
		t.Errorf("instance with fresh heartbeat should stay running, got %v", states)
	}
}

// An instance whose heartbeats stop for missedHeartbeatLimit cadences is
// replaced on reconcile.
func TestReconcile_StaleHeartbeatReplacesInstance(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	registerRunningInstance(t, s, "spec-a", "inst-1", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat("inst-1", "node-1", "running", clk.Now())

	clk.Advance(time.Duration(missedHeartbeatLimit+1) * HeartbeatCadence)
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}

	states := instanceStates(t, s, "spec-a")
	if _, alive := states["inst-1"]; alive {
		t.Errorf("stale instance should have been removed, got %v", states)
	}
	if len(states) != 1 {
		t.Errorf("a replacement instance should exist, got %v", states)
	}
}

// A heartbeat reporting a failed state replaces the instance immediately,
// without waiting for staleness.
func TestReconcile_FailedHeartbeatReplacesImmediately(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	registerRunningInstance(t, s, "spec-a", "inst-1", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat("inst-1", "node-1", "failed", clk.Now())

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states := instanceStates(t, s, "spec-a")
	if _, alive := states["inst-1"]; alive {
		t.Errorf("failed instance should have been removed, got %v", states)
	}
}

// A brand-new leader must not fail instances it simply has not heard from
// yet: staleness only counts from when this scheduler started leading.
func TestReconcile_LeaderGraceSuppressesStaleness(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	registerRunningInstance(t, s, "spec-a", "inst-1", "node-1")
	ctx := context.Background()

	// No heartbeat ever observed; first reconcile sets the baseline.
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	clk.Advance(HeartbeatCadence) // within grace
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states := instanceStates(t, s, "spec-a")
	if states["inst-1"] != "running" {
		t.Errorf("instance inside leader grace should survive, got %v", states)
	}

	// Past the grace with still no heartbeat: now it is genuinely stale.
	clk.Advance(time.Duration(missedHeartbeatLimit+1) * HeartbeatCadence)
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states = instanceStates(t, s, "spec-a")
	if _, alive := states["inst-1"]; alive {
		t.Errorf("instance past leader grace with no heartbeat should be replaced, got %v", states)
	}
}

// A failed node-dispatch RPC marks the instance failed instead of leaving
// a pending ghost.
func TestCreateInstance_RPCFailureMarksFailed(t *testing.T) {
	s, _ := newHeartbeatScheduler(t) // nil clientFactory: dispatch always fails
	ctx := context.Background()
	if err := s.RegisterAgent(ctx, &goblinv1.AgentSpec{Id: "spec-a", Replicas: 1}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		states := instanceStates(t, s, "spec-a")
		failed := false
		for _, st := range states {
			if st == "failed" {
				failed = true
			}
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatch failure should mark the instance failed, got %v", states)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
