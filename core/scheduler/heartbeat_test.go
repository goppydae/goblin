package scheduler

import (
	"context"
	"testing"
	"time"

	gapiclock "github.com/goppydae/gapi/core/clock"
	"github.com/goppydae/goblin/internal/ident"
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

// registerRunningInstance registers a spec under name and one running
// instance on nodeID (admitted and transitioned like the real flow),
// returning the canonical UUID strings of both.
func registerRunningInstance(t *testing.T, s *Scheduler, name, nodeID string) (specID, instanceID string) {
	t.Helper()
	ctx := context.Background()
	spec := &goblinv1.AgentSpec{Name: name, Replicas: 1}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	instUUID := ident.NewV7()
	if err := s.Store().Admit(ctx, spec.SpecUuid, instUUID, nodeID); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""); err != nil {
		t.Fatalf("TransitionInstance: %v", err)
	}
	return ident.String(spec.SpecUuid), ident.String(instUUID)
}

func instanceStates(t *testing.T, s *Scheduler, specID string) map[string]goblinv1.InstanceState {
	t.Helper()
	instances, err := s.ListInstances(context.Background(), specID)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	out := map[string]goblinv1.InstanceState{}
	for _, inst := range instances {
		out[ident.String(inst.InstanceUuid)] = inst.State
	}
	return out
}

// A fresh heartbeat keeps a running instance alive across reconciles.
func TestReconcile_FreshHeartbeatKeepsInstance(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	specID, instID := registerRunningInstance(t, s, "spec-a", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil { // leader grace baseline
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat(instID, "node-1", "running", clk.Now())
	clk.Advance(HeartbeatCadence)
	s.ObserveHeartbeat(instID, "node-1", "running", clk.Now())

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states := instanceStates(t, s, specID)
	if states[instID] != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("instance with fresh heartbeat should stay running, got %v", states)
	}
}

// An instance whose heartbeats stop for missedHeartbeatLimit cadences is
// replaced on reconcile.
func TestReconcile_StaleHeartbeatReplacesInstance(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	specID, instID := registerRunningInstance(t, s, "spec-a", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat(instID, "node-1", "running", clk.Now())

	clk.Advance(time.Duration(missedHeartbeatLimit+1) * HeartbeatCadence)
	// Pass 1 terminates the stale instance and admits a replacement;
	// pass 2 archives the tombstoned record.
	for i := 0; i < 2; i++ {
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents: %v", err)
		}
	}

	states := instanceStates(t, s, specID)
	if _, alive := states[instID]; alive {
		t.Errorf("stale instance should have been archived away, got %v", states)
	}
	if len(states) == 0 {
		t.Errorf("a replacement instance should exist, got %v", states)
	}
}

// A heartbeat reporting a failed state replaces the instance immediately,
// without waiting for staleness.
func TestReconcile_FailedHeartbeatReplacesImmediately(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	specID, instID := registerRunningInstance(t, s, "spec-a", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	s.ObserveHeartbeat(instID, "node-1", "failed", clk.Now())

	for i := 0; i < 2; i++ { // terminate, then archive
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents: %v", err)
		}
	}
	states := instanceStates(t, s, specID)
	if _, alive := states[instID]; alive {
		t.Errorf("failed instance should have been archived away, got %v", states)
	}
}

// A brand-new leader must not fail instances it simply has not heard from
// yet: staleness only counts from when this scheduler started leading.
func TestReconcile_LeaderGraceSuppressesStaleness(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	specID, instID := registerRunningInstance(t, s, "spec-a", "node-1")
	ctx := context.Background()

	// No heartbeat ever observed; first reconcile sets the baseline.
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	clk.Advance(HeartbeatCadence) // within grace
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}
	states := instanceStates(t, s, specID)
	if states[instID] != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("instance inside leader grace should survive, got %v", states)
	}

	// Past the grace with still no heartbeat: now it is genuinely stale.
	clk.Advance(time.Duration(missedHeartbeatLimit+1) * HeartbeatCadence)
	for i := 0; i < 2; i++ { // terminate, then archive
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents: %v", err)
		}
	}
	states = instanceStates(t, s, specID)
	if _, alive := states[instID]; alive {
		t.Errorf("instance past leader grace with no heartbeat should be replaced, got %v", states)
	}
}

// A failed node-dispatch RPC marks the instance failed instead of leaving
// a pending ghost.
func TestCreateInstance_RPCFailureMarksFailed(t *testing.T) {
	s, _ := newHeartbeatScheduler(t) // nil clientFactory: dispatch always fails
	ctx := context.Background()
	spec := &goblinv1.AgentSpec{Name: "spec-a", Replicas: 1}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		states := instanceStates(t, s, ident.String(spec.SpecUuid))
		failed := false
		for _, st := range states {
			if st == goblinv1.InstanceState_INSTANCE_STATE_TERMINATED {
				failed = true
			}
		}
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatch failure should mark the instance terminated, got %v", states)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// GOBLIN-DIV-049: a migrating instance must not be recovered.
//
// A migration checkpoints the source, which STOPS the process on
// purpose, and the node duly reports it failed. Heartbeat cannot tell a
// deliberate stop from a crash, so the reconciler terminated the
// instance and admitted a replacement while the coordinator was still
// moving it - two copies of one instance, which is a split brain rather
// than a move.
//
// This test lives here rather than beside the other reconcile tests
// because only this harness can make an instance genuinely look dead.
// The first version of it did not: it marked an instance as migrating
// without ever making it unhealthy, so no recovery was attempted with
// or without the fix, and it passed against the unfixed code. A test
// that cannot fail proves nothing.
func TestReconcile_MigratingInstanceIsNotRecovered(t *testing.T) {
	s, clk := newHeartbeatScheduler(t)
	specID, instID := registerRunningInstance(t, s, "spec-migrating", "node-1")
	ctx := context.Background()

	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents: %v", err)
	}

	// The coordinator recorded its intent through Raft before touching
	// anything; its checkpoint then stops the process and node-1
	// reports the failure. Both facts are true at once, which is the
	// whole difficulty.
	store, ok := s.Store().(*MockStore)
	if !ok {
		t.Fatalf("Store() is %T, want *MockStore", s.Store())
	}
	instUUID, perr := ident.Parse(instID)
	if perr != nil {
		t.Fatalf("parse instance id %q: %v", instID, perr)
	}
	store.SetMigrating(instUUID, "node-2")
	s.ObserveHeartbeat(instID, "node-1", "failed", clk.Now())

	for i := 0; i < 2; i++ { // terminate, then archive, if it were going to
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents: %v", err)
		}
	}

	states := instanceStates(t, s, specID)
	if _, alive := states[instID]; !alive {
		t.Fatalf("the migrating instance was recovered away while the coordinator "+
			"was still moving it; states = %v", states)
	}
	if len(states) != 1 {
		t.Fatalf("%d instances after reconciling a migrating one, want exactly 1 - "+
			"a migration that leaves a copy behind is a split brain, not a move: %v",
			len(states), states)
	}
}
