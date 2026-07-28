package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
)

func TestReconcileScaleUp(t *testing.T) {
	mockStore := NewMockStore()

	// Setup Cluster with 2 nodes
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
		{Name: "node-2", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	mockCluster := NewMockCluster(nodes)

	s := NewScheduler(mockStore, mockCluster, nil, nil, nil)
	ctx := context.Background()

	// 1. Register Spec (Replicas=3)
	spec := &goblinv1.AgentSpec{
		Name:      "agent-scale",
		Replicas:  3,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// 2. Reconcile
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}

	// 3. Verify Instances Created
	instances, err := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}

	if len(instances) != 3 {
		t.Errorf("Expected 3 instances, got %d", len(instances))
	}

	for _, inst := range instances {
		if inst.State != goblinv1.InstanceState_INSTANCE_STATE_ADMITTED &&
			inst.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING { // Stub starts it async
			t.Errorf("Instance %s in unexpected state: %v", ident.String(inst.InstanceUuid), inst.State)
		}
	}
}

func TestReconcileScaleDown(t *testing.T) {
	mockStore := NewMockStore()
	mockCluster := NewMockCluster(nil) // Cluster not needed for downscaling if logic is simple
	s := NewScheduler(mockStore, mockCluster, nil, nil, nil)
	ctx := context.Background()

	// 1. Register Spec (Replicas=1)
	spec := &goblinv1.AgentSpec{Name: "agent-down", Replicas: 1}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// 2. Create 3 existing running instances
	for i := 0; i < 3; i++ {
		instUUID := ident.NewV7()
		if err := s.Store().Admit(ctx, spec.SpecUuid, instUUID, "n1"); err != nil {
			t.Fatalf("Admit %d: %v", i, err)
		}
		if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""); err != nil {
			t.Fatalf("TransitionInstance %d: %v", i, err)
		}
	}

	// 3. Reconcile twice: pass 1 terminates the excess (tombstoning
	// them), pass 2 archives the terminated records.
	for i := 0; i < 2; i++ {
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents failed: %v", err)
		}
	}

	// 4. Verify count = 1
	instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	if len(instances) != 1 {
		t.Errorf("Expected 1 instance after scale down, got %d", len(instances))
	}
}

// TestRunReconcilerLeaderGate verifies R7: only the leader reconciles.
// Followers must skip ticks entirely; a leadership flip mid-run starts
// reconciliation without a restart.
func TestRunReconcilerLeaderGate(t *testing.T) {
	mockStore := NewMockStore()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	mockCluster := NewMockCluster(nodes)

	var leader atomic.Bool // starts false: follower
	s := NewScheduler(mockStore, mockCluster, nil, func() bool { return leader.Load() }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := &goblinv1.AgentSpec{
		Name:      "agent-gated",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	go s.RunReconciler(ctx, 5*time.Millisecond)

	// Follower: several tick intervals pass, nothing may be scheduled.
	time.Sleep(50 * time.Millisecond)
	if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) != 0 {
		t.Fatalf("follower reconciled: %d instances created, want 0", len(instances))
	}

	// Become leader: reconciliation must start without a restart.
	leader.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	t.Fatalf("leader did not reconcile: %d instances, want 1", len(instances))
}

// TestNewSchedulerNilLeaderGate verifies standalone semantics: a nil
// predicate means always-leader (single-node mode).
func TestNewSchedulerNilLeaderGate(t *testing.T) {
	mockStore := NewMockStore()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	s := NewScheduler(mockStore, NewMockCluster(nodes), nil, nil, nil)
	ctx := context.Background()

	spec := &goblinv1.AgentSpec{
		Name:      "agent-standalone",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}
	if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) != 1 {
		t.Fatalf("standalone reconcile: %d instances, want 1", len(instances))
	}
}
