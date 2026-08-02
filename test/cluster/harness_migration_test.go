//go:build cluster

// Migration readiness probing for the cluster harness.
//
// Separate from harness_test.go because that file is at its length
// limit, and because these two are one cohesive thing: the destination
// side of a migration, which is the only part of the cluster contract
// that needs applied state on a node other than the leader.
package main_test

import (
	"fmt"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// migrationReady probes one node's willingness to accept a migration.
//
// It calls the SAME pre-flight the coordinator runs (GOBLIN-DIV-048)
// rather than a proxy for it, so a harness that waits on this waits on
// exactly the condition the migration will be judged against. A probe
// that tested something adjacent would drift from the real gate, which
// is the failure GOBLIN-DIV-048 exists to prevent.
func (c *testCluster) migrationReady(node *clusterNode, instanceID string) (*goblinv1.NodeMigrationReadyResponse, error) {
	c.t.Helper()
	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close migration-ready client: %v", cerr)
		}
	}()
	req := &goblinv1.NodeMigrationReadyRequest{InstanceId: instanceID}
	var resp goblinv1.NodeMigrationReadyResponse
	err := cl.Call("NodeRPC.MigrationReady", req, &resp)
	return &resp, err
}

// waitMigrationReady polls a prospective destination until it reports
// ready, and returns how long that took.
//
// GOBLIN-DIV-059. Serf membership and an elected leader - everything
// waitLeader establishes - say nothing about whether a FOLLOWER has
// applied the operator key registry, because that registry arrives
// through the raft log. Every other cluster test drives the leader,
// which applies its own commits by construction; migration is the only
// one that needs applied state on the OTHER node, and it is the only
// one that flaked.
//
// The error carries the raft indices the response already ships,
// because "not caught up" and "caught up but empty" want opposite
// fixes and a bare timeout cannot tell them apart.
func (c *testCluster) waitMigrationReady(node *clusterNode, instanceID string, timeout time.Duration) (time.Duration, error) {
	c.t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		last, err := c.migrationReady(node, instanceID)
		if err == nil && last.GetReady() {
			return time.Since(start), nil
		}
		if time.Now().After(deadline) {
			return time.Since(start), fmt.Errorf(
				"%s never became ready to accept a migration within %s: %q (registry serial %d, applied index %d, commit index %d, rpc err: %v)",
				node.id, timeout, last.GetReason(), last.GetRegistrySerial(),
				last.GetAppliedIndex(), last.GetCommitIndex(), err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
