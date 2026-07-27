package agentmgr

import (
	"errors"

	"github.com/goppydae/gapi/internal/toposort"
)

// ErrCycle mirrors toposort.ErrCycle at this package's boundary; only HARD
// dependency cycles are errors.
var ErrCycle = toposort.ErrCycle

// TopologicalSort orders agents deps-first through the shared toposort
// (review R5: one implementation). Requires() edges order and cycle-reject;
// Wants() edges order when satisfiable and are dropped - never blocking -
// when they would form a cycle (review R14: soft deps must not block; the
// lifecycle controller separately tolerates soft-dep start failures).
func TopologicalSort(agents map[string]Agent) ([]string, error) {
	hard := make(map[string][]string, len(agents))
	soft := make(map[string][]string, len(agents))
	for id, a := range agents {
		hard[id] = a.Requires()
		soft[id] = a.Wants()
	}
	order, err := toposort.Sort(hard, soft)
	if err != nil {
		if errors.Is(err, toposort.ErrCycle) {
			return nil, ErrCycle
		}
		return nil, err
	}
	return order, nil
}
