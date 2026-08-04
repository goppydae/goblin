// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/serf/serf"
)

// Schedule selects a node for the job based on strategy.
func (s *Scheduler) Schedule(job *Job, strategy Strategy) (string, error) {
	members := s.cluster.Members()
	if len(members) == 0 {
		return "", fmt.Errorf("no nodes available in cluster")
	}

	// 1. Filter nodes by Liveness and Constraints
	var candidates []serf.Member
	for _, m := range members {
		if m.Status != 1 { // MemberStatusAlive
			continue
		}
		if !checkConstraints(m, job.Constraints) {
			continue
		}
		candidates = append(candidates, m)
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no suitable nodes found matching constraints")
	}

	// 2. Filter nodes by Resource Capacity
	// Calculate current usage for all candidates first.
	// Map: nodeID -> {usedCPU, usedMem}
	usageMap, err := s.calculateUsage(context.Background(), candidates)
	if err != nil {
		// Log warning but proceed? Or fail? Proceeds with zero usage is risky.
		// For MVP, if KV fails, we might just assume empty usage or fail.
		return "", fmt.Errorf("failed to calculate cluster usage: %w", err)
	}

	var capableNodes []serf.Member
	for _, m := range candidates {
		if hasCapacity(m, usageMap[m.Name], job.Resources) {
			capableNodes = append(capableNodes, m)
		}
	}

	if len(capableNodes) == 0 {
		return "", fmt.Errorf("insufficient capacity in cluster for job %s", job.ID)
	}

	// 3. Apply Strategy
	switch strategy {
	case StrategyRandom, StrategyRoundRobin:
		// Use local random source instead of global Seed (deprecated)
		src := rand.NewSource(time.Now().UnixNano())
		r := rand.New(src)

		// Shuffle nodes
		r.Shuffle(len(capableNodes), func(i, j int) {
			capableNodes[i], capableNodes[j] = capableNodes[j], capableNodes[i]
		})
		return capableNodes[0].Name, nil

	case StrategyLeastLoaded:
		// Pick node with lowest % resource utilization (avg of cpu% and mem%)
		return selectLeastLoaded(capableNodes, usageMap), nil

	case StrategyBinPack:
		// Pick node with highest utilization that still fits the job
		return selectBinPack(capableNodes, usageMap), nil

	default:
		return "", fmt.Errorf("unknown strategy: %s", strategy)
	}
}

func checkConstraints(m serf.Member, constraints map[string]string) bool {
	for k, v := range constraints {
		if mVal, ok := m.Tags[k]; !ok || mVal != v {
			return false
		}
	}
	return true
}

type nodeUsage struct {
	cpuUsed  float64
	memUsed  int64
	cpuTotal float64
	memTotal int64
	jobCount int
}

func (s *Scheduler) calculateUsage(ctx context.Context, nodes []serf.Member) (map[string]nodeUsage, error) {
	// This is expensive: scan all assignments then fetch specs.
	// Optimally: Maintain /stats/usage/nodeID counters in logic.
	// For MVP: Scan all assignments.

	usage := make(map[string]nodeUsage)

	// Initialize totals from tags
	for _, m := range nodes {
		cpu, _ := strconv.ParseFloat(m.Tags["cpu"], 64)
		mem, _ := strconv.ParseInt(m.Tags["memory"], 10, 64)
		usage[m.Name] = nodeUsage{cpuTotal: cpu, memTotal: mem}
	}

	// Scan all assignments: /jobs/assignments/ -> key is /jobs/assignments/<node>/<job>
	assignments, err := s.store.Scan(ctx, "default", "/jobs/assignments/")
	if err != nil {
		return nil, err
	}

	for key, jobIDBytes := range assignments {
		// Key: /jobs/assignments/<node>/<job>
		parts := strings.Split(key, "/")
		if len(parts) < 4 {
			continue
		}
		nodeID := parts[3]
		jobID := string(jobIDBytes)

		u, ok := usage[nodeID]
		if !ok {
			continue // Node not in our candidate list (maybe dead)
		}

		// Fetch job spec
		specKey := fmt.Sprintf("/jobs/specs/%s", jobID)
		specData, found, _ := s.store.Get(ctx, "default", specKey)
		if found {
			var job Job
			if err := json.Unmarshal(specData, &job); err == nil {
				u.cpuUsed += job.Resources.CPU
				u.memUsed += job.Resources.Memory
			}
		}
		u.jobCount++
		usage[nodeID] = u
	}
	return usage, nil
}

func hasCapacity(m serf.Member, u nodeUsage, req ResourceReq) bool {
	if u.cpuTotal > 0 && (u.cpuUsed+req.CPU > u.cpuTotal) {
		return false
	}
	if u.memTotal > 0 && (u.memUsed+req.Memory > u.memTotal) {
		return false
	}
	return true
}

func selectLeastLoaded(nodes []serf.Member, usage map[string]nodeUsage) string {
	bestNode := ""
	minScore := 101.0 // > 100%

	for _, n := range nodes {
		u := usage[n.Name]
		cpuPct := 0.0
		if u.cpuTotal > 0 {
			cpuPct = u.cpuUsed / u.cpuTotal
		}
		memPct := 0.0
		if u.memTotal > 0 {
			memPct = float64(u.memUsed) / float64(u.memTotal)
		}
		score := (cpuPct + memPct) / 2.0 // Simple avg

		if score < minScore {
			minScore = score
			bestNode = n.Name
		}
	}
	return bestNode
}

func selectBinPack(nodes []serf.Member, usage map[string]nodeUsage) string {
	bestNode := ""
	maxScore := -1.0

	for _, n := range nodes {
		u := usage[n.Name]
		cpuPct := 0.0
		if u.cpuTotal > 0 {
			cpuPct = u.cpuUsed / u.cpuTotal
		}
		memPct := 0.0
		if u.memTotal > 0 {
			memPct = float64(u.memUsed) / float64(u.memTotal)
		}
		score := (cpuPct + memPct) / 2.0

		if score > maxScore {
			maxScore = score
			bestNode = n.Name
		}
	}
	return bestNode
}
