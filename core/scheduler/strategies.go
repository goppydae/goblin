package scheduler

import (
	"fmt"
	"math/rand"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// SpreadStrategy distributes agents evenly across nodes (least allocated first).
type SpreadStrategy struct{}

func NewSpreadStrategy() *SpreadStrategy {
	return &SpreadStrategy{}
}

func (s *SpreadStrategy) Name() string {
	return "spread"
}

func (s *SpreadStrategy) Select(spec *goblinv1.AgentSpec, candidates []CandidateNode) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("no candidates available")
	}

	bestNode := ""
	minCount := 1000000 // Arbitrary large number

	// Shuffle candidates to handle ties randomly
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	for _, node := range candidates {
		if node.Resources.AgentCount < minCount {
			minCount = node.Resources.AgentCount
			bestNode = node.ID
		}
	}

	return bestNode, nil
}

// BinpackStrategy packs agents into the most utilized nodes that still fit (most used CPU/Mem first).
type BinpackStrategy struct{}

func NewBinpackStrategy() *BinpackStrategy {
	return &BinpackStrategy{}
}

func (s *BinpackStrategy) Name() string {
	return "binpack"
}

func (s *BinpackStrategy) Select(spec *goblinv1.AgentSpec, candidates []CandidateNode) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("no candidates available")
	}

	bestNode := ""
	maxScore := -1.0

	// Shuffle first for tie breaking
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})

	for _, node := range candidates {
		score := calculateBinpackScore(node.Resources)
		if score > maxScore {
			maxScore = score
			bestNode = node.ID
		}
	}

	return bestNode, nil
}

func calculateBinpackScore(res NodeResources) float64 {
	cpuPct := 0.0
	if res.TotalCPU > 0 {
		cpuPct = res.UsedCPU / res.TotalCPU
	}
	memPct := 0.0
	if res.TotalMem > 0 {
		memPct = float64(res.UsedMem) / float64(res.TotalMem)
	}
	// Simple average of usage. Higher is better for binpacking (filling nodes).
	return (cpuPct + memPct) / 2.0
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
