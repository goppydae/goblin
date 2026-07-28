package scheduler

import (
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
)

func TestPlacementEngine(t *testing.T) {
	pe := NewPlacementEngine()

	// Setup candidates
	nodes := []CandidateNode{
		{
			ID: "node-1",
			Resources: NodeResources{
				TotalCPU:   4.0,
				UsedCPU:    1.0,
				TotalMem:   8192,
				UsedMem:    2048,
				AgentCount: 5,
			},
			Tags: map[string]string{"region": "us-east"},
		},
		{
			ID: "node-2",
			Resources: NodeResources{
				TotalCPU:   8.0,
				UsedCPU:    7.0,
				TotalMem:   16384,
				UsedMem:    15000,
				AgentCount: 10,
			},
			Tags: map[string]string{"region": "us-west"},
		},
		{
			ID: "node-3",
			Resources: NodeResources{
				TotalCPU:   4.0,
				UsedCPU:    0.5,
				TotalMem:   8192,
				UsedMem:    1024,
				AgentCount: 1,
			},
			Tags: map[string]string{"region": "us-east"},
		},
	}

	// Test 1: Constraint Matching
	specConstraints := &goblinv1.AgentSpec{
		Name: "constrained-agent",
		Constraints: map[string]string{
			"region": "us-east",
		},
	}
	// Should pick node-3 (least loaded of us-east) or node-1
	// node-3 has 1 agent, node-1 has 5. Spread should pick node-3.
	specConstraints.Strategy = "spread"

	selected, err := pe.SelectNode(specConstraints, nodes)
	if err != nil {
		t.Fatalf("Constraint selection failed: %v", err)
	}
	if selected != "node-3" {
		t.Errorf("Expected node-3 (count=1), got %s", selected)
	}

	// Test 2: Resource Capacity
	specBig := &goblinv1.AgentSpec{
		Name: "big-agent",
		Resources: &goblinv1.ResourceReq{
			Cpu:    2.0,
			Memory: 4096,
		},
	}
	// node-2 has 1.0 CPU free (8-7). Need 2.0.
	// node-1 has 3.0 CPU free.
	// node-3 has 3.5 CPU free.
	// node-2 should be filtered out.
	// We expect node-3 due to spread (agent count 1 vs 5)

	selected, err = pe.SelectNode(specBig, nodes)
	if err != nil {
		t.Fatalf("Resource capacity test failed: %v", err)
	}
	if selected == "node-2" {
		t.Errorf("Expected node-2 to be filtered out due to CPU constraint, but got it")
	}
	if selected != "node-3" {
		// Spread logic: node-1 (count 5), node-3 (count 1). Should pick node-3.
		t.Errorf("Expected node-3 (least loaded capable), got %s", selected)
	}

	// Test 3: Binpack Strategy
	specBinpack := &goblinv1.AgentSpec{
		Name:     "binpack-agent",
		Strategy: "binpack",
		Resources: &goblinv1.ResourceReq{
			Cpu: 0.1,
		},
	}
	// node-2 usage: 7/8 = 87.5%
	// node-1 usage: 1/4 = 25%
	// node-3 usage: 0.5/4 = 12.5%
	// Binpack should prefer node-2 (highest usage) IF it fits.
	// node-2 free: 1.0. Req: 0.1. Fits.

	selected, err = pe.SelectNode(specBinpack, nodes)
	if err != nil {
		t.Fatalf("Binpack selection failed: %v", err)
	}
	if selected != "node-2" {
		t.Errorf("Binpack expected node-2 (highest utilization), got %s", selected)
	}

	// Test 4: Insufficient Resources
	specHuge := &goblinv1.AgentSpec{
		Name: "huge-agent",
		Resources: &goblinv1.ResourceReq{
			Cpu: 100.0,
		},
	}
	_, err = pe.SelectNode(specHuge, nodes)
	if err == nil {
		t.Error("Expected error for insufficient resources, got nil")
	}
}
