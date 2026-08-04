// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/goblin/internal/logattr"
)

// Boot targets: the selection mechanism Phase 2 was missing
// (GOBLIN-DIV-050).
//
// The architecture doc has always described six ordered boot phases with
// "Phase 2 - Local agents" among them, and nothing started a local agent.
// The obvious fix - call the kernel's StartAll after discovery - was
// explicitly rejected by the entry, and the reason is worth restating
// because it is what shapes this file: discovery registers every agent it
// finds and enablement defaults to on, so an unfiltered start runs
// cluster-schedulable templates as node-local processes on every node,
// AND the scheduler places instances of the same templates. Duplicate
// execution, measured: the cluster harness installs a sleeper fixture and
// passes its directory to every node, so StartAll would start a sleeper
// everywhere.
//
// So the exit is the SELECTION, not the start loop. An agent is
// boot-local if it is WantedBy a target on the boot path, and
// cluster-schedulable if it is not. That distinction falls out of the
// dependency graph the kernel already parses instead of being a
// classification someone has to invent and keep in sync.
//
// The targets mirror the documented phases and are named for what is TRUE
// when each is reached, because that is what an agent author has to
// reason about. Phases 0 and 1 are not attachment points: the agent
// manager itself comes up in Phase 1, so nothing can be waiting on it.
const (
	// TargetLocal is reached when the local runtime is up and no network
	// is required (Phase 2).
	TargetLocal = "local.target"

	// TargetNetworkReady is reached when a local agent has published
	// network readiness (Phase 3). This target is what gives that gate a
	// producer - it had none, which is why a nonzero timeout always
	// expired.
	TargetNetworkReady = "network-ready.target"

	// TargetCluster is reached when Serf membership and Raft consensus
	// are up (Phase 4).
	TargetCluster = "cluster.target"

	// TargetDistributed is reached when the scheduler is available
	// (Phase 5).
	TargetDistributed = "distributed.target"
)

// bootTargets are the targets in the order the boot path reaches them.
// Declared as a list so the ordering is a value that can be asserted on
// rather than a shape spread across call sites.
var bootTargets = []string{
	TargetLocal,
	TargetNetworkReady,
	TargetCluster,
	TargetDistributed,
}

// isBootTarget reports whether name is one of the boot-path targets.
func isBootTarget(name string) bool {
	for _, t := range bootTargets {
		if t == name {
			return true
		}
	}
	return false
}

// agentsForTarget selects the agents to start when target is reached.
//
// The seed is every agent naming target in its WantedBy. The set is then
// closed transitively over REQUIRES, because a hard dependency of a
// boot-local agent must also be boot-local - otherwise the kernel's sort
// silently drops the edge (it ignores edges naming agents outside the map
// it was given) and the agent starts without the thing it declared it
// could not run without. That failure is quiet, which is why the closure
// is here rather than left to the sort.
//
// It does NOT close over WANTS. systemd pulls in both, and this
// deliberately does not: a soft edge pulling a cluster-schedulable
// template into a boot target is exactly the duplicate execution this
// whole mechanism exists to avoid. Wants still ORDERS when both agents
// are in the set; it just never recruits one. Widening this later is
// additive; narrowing it after agents rely on it would not be.
func agentsForTarget(all map[string]gapiagentmgr.Agent, target string) map[string]gapiagentmgr.Agent {
	selected := make(map[string]gapiagentmgr.Agent)

	var pull func(id string)
	pull = func(id string) {
		if _, done := selected[id]; done {
			return
		}
		a, known := all[id]
		if !known {
			// A Requires naming something undiscovered. Not this
			// function's error to raise: the kernel's sort already
			// ignores unknown edges, and refusing here would make one
			// missing optional agent block an otherwise healthy boot.
			return
		}
		selected[id] = a
		for _, dep := range a.Requires() {
			pull(dep)
		}
	}

	for id, a := range all {
		for _, w := range a.WantedBy() {
			if w == target {
				pull(id)
				break
			}
		}
	}
	return selected
}

// startAgent is how reachTarget starts one agent. It is a parameter
// rather than a direct call so a test can observe exactly which agents a
// target starts, which is the assertion GOBLIN-DIV-050 closes on.
type startAgent func(id string, a gapiagentmgr.Agent) error

// applyStart is the production startAgent: drive the agent's lifecycle
// controller, the same path the kernel's own supervisor uses.
func applyStart(_ string, a gapiagentmgr.Agent) error {
	return a.Controller().Apply(gapilifecycle.ActionStart)
}

// reachTarget starts the agents wanted by target, dependencies first.
//
// Ordering comes from the kernel's topological sort over the SELECTED
// subset, so Requires orders and cycle-rejects while Wants orders when
// satisfiable and drops rather than blocking - one implementation of
// those semantics, not a second one here.
//
// A start failure is logged and does not abort the target. That matches
// how the kernel's lifecycle controller already treats a soft-dependency
// failure, and the alternative is worse: one broken node-local agent
// would refuse the whole boot, including the cluster join that an
// operator needs in order to fix it.
func (s *Supervisor) reachTarget(ctx context.Context, st *runState, target string, start startAgent) error {
	if st.agentMgr == nil {
		return nil // no local runtime on this node
	}
	selected := agentsForTarget(st.agentMgr.All(), target)
	if len(selected) == 0 {
		return nil
	}

	order, err := gapiagentmgr.TopologicalSort(selected)
	if err != nil {
		// A cycle among boot-local agents is a configuration error the
		// operator must see, and starting a subset in an arbitrary order
		// would be worse than not starting.
		return fmt.Errorf("target %s: %w", target, err)
	}

	started := 0
	for _, id := range order {
		a, ok := selected[id]
		if !ok {
			continue
		}
		if serr := start(id, a); serr != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "boot-local agent failed to start",
				logattr.AgentID(id), slog.String("target", target), logattr.Err(serr))
			continue
		}
		started++
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "target reached",
		slog.String("target", target), slog.Int("agents", started))
	return nil
}

// declaredTargets returns every target name any discovered agent names in
// WantedBy, sorted. Used to report memberships that name a target the
// boot path never reaches, which would otherwise be silent: the agent
// simply never starts and nothing says why.
func declaredTargets(all map[string]gapiagentmgr.Agent) []string {
	seen := map[string]struct{}{}
	for _, a := range all {
		for _, w := range a.WantedBy() {
			seen[w] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// warnUnreachableTargets reports agents whose WantedBy names a target the
// boot path never reaches.
//
// Without this the failure is silent in the worst way: the agent is
// discovered, registered, enabled, and simply never starts, and nothing
// anywhere says why. A typo in a target name is indistinguishable from a
// deliberately cluster-schedulable agent unless something looks.
func (s *Supervisor) warnUnreachableTargets(ctx context.Context, st *runState) {
	if st.agentMgr == nil {
		return
	}
	for _, t := range declaredTargets(st.agentMgr.All()) {
		if isBootTarget(t) {
			continue
		}
		slog.Default().LogAttrs(ctx, slog.LevelWarn,
			"agent declares wanted_by a target the boot path never reaches; it will not start at boot",
			slog.String("target", t), slog.Any("boot_targets", bootTargets))
	}
}
