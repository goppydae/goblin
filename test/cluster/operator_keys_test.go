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
	"strings"
	"testing"
	"time"

	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// TestKeylessClusterRefusesMutations is the piece-1 property
// (GOBLIN-DIV-015): with no operator key registered, a mutating verb is
// refused. The test starts a node WITHOUT a key rather than emptying a
// registry afterwards, because a populated registry can never go back to
// empty - removing the last key is refused by the FSM.
func TestKeylessClusterRefusesMutations(t *testing.T) {
	c, node := startKeylessNode(t, "keyless")
	c.waitLeader(node, 1, 60*time.Second)

	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			t.Logf("close client: %v", cerr)
		}
	}()

	req := &goblinv1.RegisterGlobalAgentRequest{
		Spec: &goblinv1.AgentSpec{Name: "refused-spec", Type: "sleeper", Replicas: 1},
	}
	var resp goblinv1.RegisterGlobalAgentResponse
	err := cl.Call("SchedulerRPC.RegisterGlobalAgent", req, &resp)
	if err == nil {
		t.Fatal("RegisterGlobalAgent succeeded on a node with no operator key")
	}
	// Assert the wire code, not the message: rpc.proto says callers
	// branch on the code and never match on text.
	if !supervisor.IsPermissionDenied(err) {
		t.Fatalf("refusal was not classified PERMISSION_DENIED: %v", err)
	}
}

// TestKeylessNodeNeverBecomesMigrationReady is the discriminating half
// of the GOBLIN-DIV-059 fix. waitMigrationReady exists so the migration
// test stops racing the registry's replication - but a helper that
// reported ready unconditionally, or that gave up on the first probe,
// would make that test pass just as well. This one disagrees with both:
// a node started with no operator key can NEVER become ready, because
// the registry it is waiting on will never arrive.
//
// It also asserts the elapsed time. The property under test is that the
// helper WAITED, and a helper that returns the right error instantly is
// still the wrong helper.
func TestKeylessNodeNeverBecomesMigrationReady(t *testing.T) {
	const window = 3 * time.Second

	c, node := startKeylessNode(t, "keyless-migration")
	c.waitLeader(node, 1, 60*time.Second)

	elapsed, err := c.waitMigrationReady(node, "", window)
	if err == nil {
		t.Fatal("a node with no operator key reported ready to accept a migration")
	}
	if elapsed < window {
		t.Fatalf("gave up after %s; it must keep polling for the full %s", elapsed, window)
	}
	if !strings.Contains(err.Error(), "operator key registry has not been applied on this node") {
		t.Fatalf("refusal did not name the missing registry: %v", err)
	}
}

// TestSeededClusterAuthorizesMutations is the matching positive: the
// same verb, on a node started with an operator key, is allowed. Without
// it, a bug that refused unconditionally would still pass the test
// above.
func TestSeededClusterAuthorizesMutations(t *testing.T) {
	c := startCluster(t, 1)
	leader := c.waitLeader(c.anyNode(), 1, 60*time.Second)
	// register t.Fatalf's on failure, which is the assertion here.
	c.register(leader, &goblinv1.AgentSpec{Name: "allowed-spec", Type: "sleeper", Replicas: 1})
}
