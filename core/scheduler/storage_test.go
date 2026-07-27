package scheduler

import (
	"context"
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
)

func TestAgentStorage(t *testing.T) {
	mockStore := NewMockStore()
	s := NewScheduler(mockStore, nil, nil, nil, nil) // Cluster/Bus not needed for storage tests
	ctx := context.Background()

	// 1. Test Register and Get
	spec := &goblinv1.AgentSpec{
		Id:       "agent-1",
		Type:     "python-trader",
		Replicas: 3,
		Resources: &goblinv1.ResourceReq{
			Cpu:    1.5,
			Memory: 1024,
		},
	}

	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	retrieved, err := s.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent failed: %v", err)
	}

	if retrieved.Id != spec.Id {
		t.Errorf("Expected ID %s, got %s", spec.Id, retrieved.Id)
	}
	if retrieved.Replicas != spec.Replicas {
		t.Errorf("Expected Replicas %d, got %d", spec.Replicas, retrieved.Replicas)
	}
	if retrieved.Resources.Cpu != spec.Resources.Cpu {
		t.Errorf("Expected CPU %f, got %f", spec.Resources.Cpu, retrieved.Resources.Cpu)
	}

	// 2. Test List
	spec2 := &goblinv1.AgentSpec{
		Id:   "agent-2",
		Type: "logger",
	}
	if err := s.RegisterAgent(ctx, spec2); err != nil {
		t.Fatalf("RegisterAgent 2 failed: %v", err)
	}

	list, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(list))
	}

	// 3. Test Delete
	if err := s.DeleteAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}

	_, err = s.GetAgent(ctx, "agent-1")
	if err == nil {
		t.Error("Expected error getting deleted agent, got nil")
	}
}

func TestInstanceStorage(t *testing.T) {
	mockStore := NewMockStore()
	s := NewScheduler(mockStore, nil, nil, nil, nil)
	ctx := context.Background()

	instance := &goblinv1.AgentInstance{
		InstanceId: "inst-1",
		SpecId:     "agent-1",
		NodeId:     "node-1",
		State:      "running",
	}

	// 1. Save and Get
	if err := s.SaveInstance(ctx, instance); err != nil {
		t.Fatalf("SaveInstance failed: %v", err)
	}

	got, err := s.GetInstance(ctx, "inst-1")
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if got.SpecId != instance.SpecId {
		t.Errorf("Expected SpecId %s, got %s", instance.SpecId, got.SpecId)
	}

	// 2. List with Filter
	inst2 := &goblinv1.AgentInstance{
		InstanceId: "inst-2",
		SpecId:     "agent-1",
		NodeId:     "node-2",
	}
	inst3 := &goblinv1.AgentInstance{
		InstanceId: "inst-3",
		SpecId:     "agent-2", // Different spec
	}

	for _, inst := range []*goblinv1.AgentInstance{inst2, inst3} {
		if err := s.SaveInstance(ctx, inst); err != nil {
			t.Fatalf("SaveInstance %s: %v", inst.InstanceId, err)
		}
	}

	// Filter by agent-1
	list, err := s.ListInstances(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Expected 2 instances for agent-1, got %d", len(list))
	}

	// No filter
	all, err := s.ListInstances(ctx, "")
	if err != nil {
		t.Fatalf("ListInstances(all) failed: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Expected 3 total instances, got %d", len(all))
	}
}
