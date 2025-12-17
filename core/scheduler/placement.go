package scheduler

import (
	"fmt"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// CandidateNode represents a node available for scheduling
type CandidateNode struct {
	ID        string
	Tags      map[string]string
	Resources NodeResources
}

type NodeResources struct {
	TotalCPU   float64
	UsedCPU    float64
	TotalMem   int64
	UsedMem    int64
	AgentCount int
}

// PlacementStrategy defines the interface for selecting a node
type PlacementStrategy interface {
	Name() string
	Select(spec *goblinv1.AgentSpec, candidates []CandidateNode) (string, error)
}

// PlacementEngine handles node selection
type PlacementEngine struct {
	strategies map[string]PlacementStrategy
}

func NewPlacementEngine() *PlacementEngine {
	pe := &PlacementEngine{
		strategies: make(map[string]PlacementStrategy),
	}
	// Register default strategies
	pe.Register(NewSpreadStrategy())
	pe.Register(NewBinpackStrategy())
	return pe
}

func (pe *PlacementEngine) Register(s PlacementStrategy) {
	pe.strategies[s.Name()] = s
}

// SelectNode finds the best node for the agent spec
func (pe *PlacementEngine) SelectNode(spec *goblinv1.AgentSpec, candidates []CandidateNode) (string, error) {
	// 1. Filter by Constraints (Hard requirements)
	filtered := make([]CandidateNode, 0, len(candidates))
	for _, node := range candidates {
		if checkGlobalConstraints(spec, node) {
			filtered = append(filtered, node)
		}
	}

	if len(filtered) == 0 {
		return "", fmt.Errorf("no nodes satisfy constraints")
	}

	// 2. Filter by Resource Capacity
	capable := make([]CandidateNode, 0, len(filtered))
	for _, node := range filtered {
		if hasResourceCapacity(spec.Resources, node.Resources) {
			capable = append(capable, node)
		}
	}

	if len(capable) == 0 {
		return "", fmt.Errorf("insufficient resources in cluster")
	}

	// 3. Apply Strategy
	strategyName := spec.Strategy
	if strategyName == "" {
		strategyName = "spread" // Default
	}

	strategy, ok := pe.strategies[strategyName]
	if !ok {
		return "", fmt.Errorf("unknown placement strategy: %s", strategyName)
	}

	return strategy.Select(spec, capable)
}

func checkGlobalConstraints(spec *goblinv1.AgentSpec, node CandidateNode) bool {
	for k, v := range spec.Constraints {
		if nodeVal, ok := node.Tags[k]; !ok || nodeVal != v {
			return false
		}
	}
	return true
}

func hasResourceCapacity(req *goblinv1.ResourceReq, avail NodeResources) bool {
	if req == nil {
		return true
	}
	if avail.TotalCPU > 0 && (avail.UsedCPU+req.Cpu > avail.TotalCPU) {
		return false
	}
	if avail.TotalMem > 0 && (avail.UsedMem+req.Memory > avail.TotalMem) {
		return false
	}
	return true
}
