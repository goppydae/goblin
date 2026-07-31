//go:build cluster

package main_test

import (
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(err.Error(), "operator key registry is empty") {
		t.Fatalf("refusal did not name the empty registry: %v", err)
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
