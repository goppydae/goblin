// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"errors"
	"testing"

	"github.com/hashicorp/raft"
)

func TestPlanBootstrap_WaitsForTheExpectedCount(t *testing.T) {
	peers := []BootstrapPeer{
		{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 3},
		{NodeID: "node-b", Addr: "10.0.0.2:7946", Expect: 3},
	}

	plan, ready, err := PlanBootstrap(3, peers)
	if err != nil {
		t.Fatalf("PlanBootstrap: %v", err)
	}
	if ready {
		t.Fatalf("plan reported ready with %d of 3 peers: %+v", len(peers), plan)
	}
}

func TestPlanBootstrap_ElectsOneBootstrapperDeterministically(t *testing.T) {
	// Every node runs this against the same membership view, so the
	// choice must not depend on the order serf happens to report.
	orderings := [][]BootstrapPeer{
		{
			{NodeID: "node-c", Addr: "10.0.0.3:7946", Expect: 3},
			{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 3},
			{NodeID: "node-b", Addr: "10.0.0.2:7946", Expect: 3},
		},
		{
			{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 3},
			{NodeID: "node-b", Addr: "10.0.0.2:7946", Expect: 3},
			{NodeID: "node-c", Addr: "10.0.0.3:7946", Expect: 3},
		},
	}

	for i, peers := range orderings {
		plan, ready, err := PlanBootstrap(3, peers)
		if err != nil {
			t.Fatalf("ordering %d: PlanBootstrap: %v", i, err)
		}
		if !ready {
			t.Fatalf("ordering %d: not ready with all 3 peers", i)
		}
		if plan.Bootstrapper != "node-a" {
			t.Errorf("ordering %d: bootstrapper = %q, want node-a", i, plan.Bootstrapper)
		}
		want := []raft.Server{
			{ID: "node-a", Address: "10.0.0.1:7946", Suffrage: raft.Voter},
			{ID: "node-b", Address: "10.0.0.2:7946", Suffrage: raft.Voter},
			{ID: "node-c", Address: "10.0.0.3:7946", Suffrage: raft.Voter},
		}
		if len(plan.Servers) != len(want) {
			t.Fatalf("ordering %d: got %d servers, want %d", i, len(plan.Servers), len(want))
		}
		for j := range want {
			if plan.Servers[j] != want[j] {
				t.Errorf("ordering %d: server %d = %+v, want %+v", i, j, plan.Servers[j], want[j])
			}
		}
	}
}

func TestPlanBootstrap_RejectsDisagreementOnTheExpectedSize(t *testing.T) {
	// Two nodes expecting 3 and one expecting 5 would bootstrap two
	// different clusters. That is a configuration fault, not a wait.
	peers := []BootstrapPeer{
		{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 3},
		{NodeID: "node-b", Addr: "10.0.0.2:7946", Expect: 3},
		{NodeID: "node-c", Addr: "10.0.0.3:7946", Expect: 5},
	}

	_, _, err := PlanBootstrap(3, peers)
	if !errors.Is(err, ErrBootstrapExpectMismatch) {
		t.Fatalf("err = %v, want ErrBootstrapExpectMismatch", err)
	}
}

func TestPlanBootstrap_RejectsDuplicateNodeIDs(t *testing.T) {
	peers := []BootstrapPeer{
		{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 2},
		{NodeID: "node-a", Addr: "10.0.0.2:7946", Expect: 2},
	}

	_, _, err := PlanBootstrap(2, peers)
	if !errors.Is(err, ErrDuplicateNodeID) {
		t.Fatalf("err = %v, want ErrDuplicateNodeID", err)
	}
}

func TestPlanBootstrap_IgnoresPeersWithoutTheTag(t *testing.T) {
	// A node that never advertised bootstrap_expect (Expect == 0) is a
	// plain joiner; it must not be counted toward the seed set nor
	// baked into the initial configuration.
	peers := []BootstrapPeer{
		{NodeID: "node-a", Addr: "10.0.0.1:7946", Expect: 2},
		{NodeID: "node-b", Addr: "10.0.0.2:7946", Expect: 2},
		{NodeID: "node-z", Addr: "10.0.0.9:7946", Expect: 0},
	}

	plan, ready, err := PlanBootstrap(2, peers)
	if err != nil {
		t.Fatalf("PlanBootstrap: %v", err)
	}
	if !ready {
		t.Fatal("not ready with both seed peers present")
	}
	if len(plan.Servers) != 2 {
		t.Fatalf("got %d servers, want 2 (node-z is a joiner)", len(plan.Servers))
	}
	for _, s := range plan.Servers {
		if s.ID == "node-z" {
			t.Error("untagged joiner baked into the initial configuration")
		}
	}
}

func TestPlanBootstrap_DisabledWhenExpectIsBelowTwo(t *testing.T) {
	// 0 keeps the seed model (JoinAddr == "" bootstraps alone); 1 is
	// the same thing said differently and must not need a peer set.
	for _, expect := range []int{0, 1} {
		_, ready, err := PlanBootstrap(expect, nil)
		if err != nil {
			t.Fatalf("expect=%d: %v", expect, err)
		}
		if ready {
			t.Errorf("expect=%d: reported ready; bootstrap-expect is not in play", expect)
		}
	}
}
