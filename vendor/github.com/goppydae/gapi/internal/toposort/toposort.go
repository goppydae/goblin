// Package toposort is the one dependency-ordering implementation shared by
// the agent registry and the agent manager (review R5: centralize; R14:
// soft-dependency semantics). Hard edges order and cycle-reject; soft edges
// order when satisfiable and are dropped - never an error, never blocking -
// when they would form a cycle ("Wants orders, never blocks").
package toposort

import (
	"errors"
	"fmt"
	"sort"
)

// ErrCycle is returned when the HARD dependency graph is cyclic. Soft
// cycles are broken silently by dropping soft edges.
var ErrCycle = errors.New("toposort: hard dependency cycle")

// Sort returns a start order for the nodes (the keys of hard). Both maps
// are node -> dependencies (things that must/should start first). Deps
// naming unknown nodes are ignored (external services). The result is
// deterministic: ties break lexicographically.
func Sort(hard, soft map[string][]string) ([]string, error) {
	known := make(map[string]bool, len(hard))
	for id := range hard {
		known[id] = true
	}

	// Phase 1: hard edges only - a cycle here is an error.
	if stuck := stuckNodes(known, edgeSet(known, hard)); len(stuck) > 0 {
		ids := append([]string(nil), stuck...)
		sort.Strings(ids)
		return nil, fmt.Errorf("%w involving %v", ErrCycle, ids)
	}

	// Phase 2: hard+soft edges; when progress stalls, drop the soft edges
	// into the stuck set and continue (the hard graph is acyclic, so this
	// always terminates with every node emitted).
	hardEdges := edgeSet(known, hard)
	softEdges := edgeSet(known, soft)
	for {
		emitted := kahn(known, merge(hardEdges, softEdges))
		if len(emitted) == len(known) {
			return emitted, nil
		}
		stuckSet := make(map[string]bool, len(known)-len(emitted))
		for id := range known {
			stuckSet[id] = true
		}
		for _, id := range emitted {
			delete(stuckSet, id)
		}
		dropped := false
		for node, deps := range softEdges {
			if !stuckSet[node] {
				continue
			}
			for dep := range deps {
				if stuckSet[dep] {
					delete(deps, dep)
					dropped = true
				}
			}
			if len(deps) == 0 {
				delete(softEdges, node)
			}
		}
		if !dropped {
			// Cannot happen with an acyclic hard graph; guard against an
			// infinite loop anyway.
			return nil, fmt.Errorf("%w: no progress after dropping soft edges", ErrCycle)
		}
	}
}

// edgeSet normalizes node -> dep lists into node -> set, dropping unknown
// deps and self-edges.
func edgeSet(known map[string]bool, deps map[string][]string) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(deps))
	for node, list := range deps {
		if !known[node] {
			continue
		}
		for _, dep := range list {
			if !known[dep] || dep == node {
				continue
			}
			if out[node] == nil {
				out[node] = make(map[string]bool)
			}
			out[node][dep] = true
		}
	}
	return out
}

func merge(a, b map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(a)+len(b))
	for _, src := range []map[string]map[string]bool{a, b} {
		for node, deps := range src {
			if out[node] == nil {
				out[node] = make(map[string]bool, len(deps))
			}
			for dep := range deps {
				out[node][dep] = true
			}
		}
	}
	return out
}

// stuckNodes returns the nodes NOT emitted by Kahn's (the cyclic residue).
func stuckNodes(known map[string]bool, edges map[string]map[string]bool) []string {
	emitted := kahn(known, edges)
	if len(emitted) == len(known) {
		return nil
	}
	seen := make(map[string]bool, len(emitted))
	for _, id := range emitted {
		seen[id] = true
	}
	var stuck []string
	for id := range known {
		if !seen[id] {
			stuck = append(stuck, id)
		}
	}
	return stuck
}

// kahn emits as many nodes as the edge set allows, deps-first,
// deterministically (lexicographic tie-break).
func kahn(known map[string]bool, edges map[string]map[string]bool) []string {
	inDeg := make(map[string]int, len(known))
	dependents := make(map[string][]string, len(known))
	for id := range known {
		inDeg[id] = 0
	}
	for node, deps := range edges {
		for dep := range deps {
			inDeg[node]++
			dependents[dep] = append(dependents[dep], node)
		}
	}

	var ready []string
	for id, d := range inDeg {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(known))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)

		var unlocked []string
		for _, dependent := range dependents[id] {
			inDeg[dependent]--
			if inDeg[dependent] == 0 {
				unlocked = append(unlocked, dependent)
			}
		}
		sort.Strings(unlocked)
		ready = append(ready, unlocked...)
	}
	return order
}
