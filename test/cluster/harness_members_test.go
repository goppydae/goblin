// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build cluster

package main_test

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// memberProbe reports how many members the named node currently sees.
// It exists so the wait below can be exercised without a cluster: the
// convergence policy is what is being tested, not the RPC.
type memberProbe func(id string) (int, error)

// waitAllMembers waits for every node to report expect members, giving
// EACH node its own budget and visiting them in a FIXED order.
//
// Both properties answer GOBLIN-DIV-064. The caller previously computed
// a single deadline OUTSIDE a loop that ranged over the node map, which
// made two things vary between runs of an identical tree: how much
// budget a later node had left after an earlier one polled, and - when
// more than one node was short - which node the failure named, because
// Go randomises map iteration order. Neither varies now.
//
// The error reports what the node ACTUALLY SAW, not merely that it was
// not expect. "saw 2" and "saw 0" are different defects, and the entry
// this fixes was one observation short of telling them apart.
func waitAllMembers(ids []string, expect int, perNode time.Duration, probe memberProbe) error {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)

	for _, id := range ordered {
		deadline := time.Now().Add(perNode)
		for {
			seen, err := probe(id)
			if err == nil && seen == expect {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s never saw %d members (saw %d, last err: %v)",
					id, expect, seen, err)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	return nil
}

// TestWaitAllMembers_NamesTheSameNodeAcrossRuns is the discriminating
// half, and it needs TWO failing nodes to discriminate at all: with one,
// every iteration order names the same node and the assertion holds
// trivially whether or not the order is fixed.
//
// With the pre-064 shape - ranging the node map - the reported node is
// whichever of the two the runtime happened to visit first, so this
// fails within a few iterations. See the sabotage recorded in the entry.
func TestWaitAllMembers_NamesTheSameNodeAcrossRuns(t *testing.T) {
	const expect = 3
	probe := func(id string) (int, error) {
		if id == "node-2" || id == "node-3" {
			return 1, nil // permanently short: both can fail
		}
		return expect, nil
	}

	for i := 0; i < 15; i++ {
		err := waitAllMembers([]string{"node-3", "node-1", "node-2"},
			expect, 300*time.Millisecond, probe)
		if err == nil {
			t.Fatalf("iteration %d: two nodes are permanently short, want an error", i)
		}
		if got := err.Error(); got[:6] != "node-2" {
			t.Fatalf("iteration %d: failure named %q, want the first short node "+
				"in sorted order (node-2) every time", i, got)
		}
	}
}

// TestWaitAllMembers_GivesEachNodeItsOwnBudget is the half that fails
// under a SHARED deadline: node-1 converges late but legitimately, and
// under one shared budget it leaves node-2 too little to converge at
// all - so a healthy node is reported as broken because of a slow
// sibling. That is the defect, independent of any flake.
func TestWaitAllMembers_GivesEachNodeItsOwnBudget(t *testing.T) {
	const expect = 3
	calls := map[string]int{}
	probe := func(id string) (int, error) {
		calls[id]++
		switch id {
		case "node-1":
			if calls[id] <= 7 { // ~1.75s of a 2s budget
				return 1, nil
			}
		case "node-2":
			if calls[id] <= 2 { // ~0.5s, well inside its OWN budget
				return 1, nil
			}
		}
		return expect, nil
	}

	if err := waitAllMembers([]string{"node-1", "node-2", "node-3"},
		expect, 2*time.Second, probe); err != nil {
		t.Fatalf("every node converges inside its own budget: %v", err)
	}
}
