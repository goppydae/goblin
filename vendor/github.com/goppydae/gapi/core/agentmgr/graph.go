package agentmgr

import "errors"

var ErrCycle = errors.New("dependency cycle detected")

type Adj struct {
	Out map[string][]string // node -> deps
	In  map[string][]string // reverse edges
}

func TopologicalSort(agents map[string]Agent) ([]string, error) {
	// Build graph: Dependency -> [Dependents]
	// If A depends on B, graph has B -> A
	adj := map[string][]string{}
	inDegree := map[string]int{}

	// Initialize
	for id := range agents {
		adj[id] = []string{}
		inDegree[id] = 0
	}

	// Populate
	for id, a := range agents {
		for _, dep := range a.Dependencies() {
			// dep -> id
			adj[dep] = append(adj[dep], id)
			inDegree[id]++

			// Ensure dep exists in map even if not in agents map (external dep?)
			// For strictly local sorting, we might ignore external or error.
			// Assuming all deps are keys in agents for now or handled.
			if _, ok := inDegree[dep]; !ok {
				inDegree[dep] = 0 // Should be covered by init loop if local
			}
		}
	}

	// Kahn's Algorithm
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	var order []string
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		order = append(order, curr)

		for _, neighbor := range adj[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(order) < len(inDegree) {
		return nil, ErrCycle
	}

	return order, nil
}
