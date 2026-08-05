// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

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
//
// WantedBy() and RequiredBy() are folded in as the REVERSE edges they
// have always claimed to be: "X is wanted_by Y" means Y wants X, so the
// edge belongs on Y. Both were parsed, validated, stored and written
// into the registry graph while this sort built its inputs from
// Requires and Wants alone, so declaring wanted_by ordered nothing.
//
// A reverse edge naming something that is not a known agent is ignored,
// matching how the sort already treats an unknown forward dep. That
// leaves a target-style anchor - a name with no agent behind it - still
// inert; making anchors real is a target-model question, not this one.
func TopologicalSort(agents map[string]Agent) ([]string, error) {
	hard := make(map[string][]string, len(agents))
	soft := make(map[string][]string, len(agents))
	// Copied, not aliased: the reverse-edge pass below appends to these,
	// and Requires()/Wants() hand back the agent's own slice.
	for id, a := range agents {
		hard[id] = append([]string(nil), a.Requires()...)
		soft[id] = append([]string(nil), a.Wants()...)
	}
	for id, a := range agents {
		for _, target := range a.WantedBy() {
			if _, known := agents[target]; known {
				soft[target] = append(soft[target], id)
			}
		}
		for _, target := range a.RequiredBy() {
			if _, known := agents[target]; known {
				hard[target] = append(hard[target], id)
			}
		}
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
