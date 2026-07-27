package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
		Id:        "agent-scale",
		Replicas:  3,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	s.RegisterAgent(ctx, spec)

	// 2. Reconcile
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}

	// 3. Verify Instances Created
	instances, err := s.ListInstances(ctx, "agent-scale")
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}

	if len(instances) != 3 {
		t.Errorf("Expected 3 instances, got %d", len(instances))
	}

	for _, inst := range instances {
		if inst.State != "pending" && inst.State != "running" { // Stub starts it async
			t.Errorf("Instance %s in unexpected state: %s", inst.InstanceId, inst.State)
		}
	}
}

func TestReconcileScaleDown(t *testing.T) {
	mockStore := NewMockStore()
	mockCluster := NewMockCluster(nil) // Cluster not needed for downscaling if logic is simple
	s := NewScheduler(mockStore, mockCluster, nil, nil, nil)
	ctx := context.Background()

	// 1. Register Spec (Replicas=1)
	spec := &goblinv1.AgentSpec{Id: "agent-down", Replicas: 1}
	s.RegisterAgent(ctx, spec)

	// 2. Create 3 existing instances
	inst1 := &goblinv1.AgentInstance{InstanceId: "i1", SpecId: "agent-down", State: "running", NodeId: "n1"}
	inst2 := &goblinv1.AgentInstance{InstanceId: "i2", SpecId: "agent-down", State: "running", NodeId: "n1"}
	inst3 := &goblinv1.AgentInstance{InstanceId: "i3", SpecId: "agent-down", State: "running", NodeId: "n1"}
	s.SaveInstance(ctx, inst1)
	s.SaveInstance(ctx, inst2)
	s.SaveInstance(ctx, inst3)

	// 3. Reconcile
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}

	// 4. Verify count = 1
	instances, _ := s.ListInstances(ctx, "agent-down")
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
		Id:        "agent-gated",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	go s.RunReconciler(ctx, 5*time.Millisecond)

	// Follower: several tick intervals pass, nothing may be scheduled.
	time.Sleep(50 * time.Millisecond)
	if instances, _ := s.ListInstances(ctx, "agent-gated"); len(instances) != 0 {
		t.Fatalf("follower reconciled: %d instances created, want 0", len(instances))
	}

	// Become leader: reconciliation must start without a restart.
	leader.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if instances, _ := s.ListInstances(ctx, "agent-gated"); len(instances) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	instances, _ := s.ListInstances(ctx, "agent-gated")
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
		Id:        "agent-standalone",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}
	if instances, _ := s.ListInstances(ctx, "agent-standalone"); len(instances) != 1 {
		t.Fatalf("standalone reconcile: %d instances, want 1", len(instances))
	}
}
