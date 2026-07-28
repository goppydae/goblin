//go:build cluster

package main_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBootstrapExpect_HoldsUntilTheSeedSetIsComplete proves the
// property that distinguishes bootstrap-expect from the seed model: no
// node seeds the cluster early, and when the set completes exactly one
// node seeds it with all members already in the configuration.
//
// The seed model bootstraps whichever node has no --join the instant it
// starts, so a two-node partition of a three-node cluster would elect a
// leader over a one-server configuration and accept writes the absent
// nodes never agreed to.
func TestBootstrapExpect_HoldsUntilTheSeedSetIsComplete(t *testing.T) {
	c := &testCluster{t: t, nodes: map[string]*clusterNode{}}

	const expect = 3
	expectArgs := []string{"--bootstrap-expect", fmt.Sprint(expect)}

	// Two of three: the set is incomplete, so nothing may seed.
	first := c.startNodeWithArgs("node-1", "", expectArgs...)
	c.startNodeWithArgs("node-2", first.listenAddr, expectArgs...)

	if node, ok := c.tryWaitLeader(first, 2, 8*time.Second); ok {
		t.Fatalf("cluster elected %s with only 2 of %d seed nodes present", node.id, expect)
	}

	// The third completes the set; now it must seed and elect.
	c.startNodeWithArgs("node-3", first.listenAddr, expectArgs...)
	leader := c.waitLeader(first, expect, 45*time.Second)
	t.Logf("leader after seed set completed: %s", leader.id)

	// Every node must converge on the same membership. node-3 has only
	// just joined, so this is a convergence wait, not a retry of a
	// failed assertion.
	deadline := time.Now().Add(30 * time.Second)
	for id, node := range c.nodes {
		for {
			members, err := c.members(node)
			if err == nil && len(members) == expect {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never saw %d members (last err: %v)", id, expect, err)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	// Exactly one node may claim the bootstrap, and it must have seeded
	// the full configuration rather than itself alone. The logs are the
	// mechanical proof: the decision is made independently on each node
	// and never coordinated over the wire.
	var bootstrappers, deferrers []string
	logDeadline := time.Now().Add(30 * time.Second)
	for {
		bootstrappers, deferrers = nil, nil
		for id, node := range c.nodes {
			log := readNodeLog(t, node)
			if strings.Contains(log, "seed set complete; bootstrapping cluster") {
				bootstrappers = append(bootstrappers, id)
				if !strings.Contains(log, `"servers":3`) {
					t.Errorf("%s bootstrapped without the full seed configuration", id)
				}
			}
			if strings.Contains(log, "seed set complete; another node bootstraps") {
				deferrers = append(deferrers, id)
			}
		}
		if len(bootstrappers) == 1 && len(deferrers) == expect-1 {
			break
		}
		if time.Now().After(logDeadline) {
			t.Fatalf("bootstrappers = %v (want exactly one), deferring = %v (want %d)",
				bootstrappers, deferrers, expect-1)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("bootstrapped by %v; %v deferred", bootstrappers, deferrers)
}

// tryWaitLeader is waitLeader without the fatal: it reports whether a
// single leader appeared inside the window, so a test can assert that
// one did NOT.
func (c *testCluster) tryWaitLeader(via *clusterNode, n int, timeout time.Duration) (*clusterNode, bool) {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		members, err := c.members(via)
		if err == nil && len(members) >= n {
			leaders := 0
			leaderName := ""
			for _, m := range members {
				if m.Leader {
					leaders++
					leaderName = m.Name
				}
			}
			if leaders == 1 {
				if node, ok := c.nodes[leaderName]; ok {
					return node, true
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil, false
}

func readNodeLog(t *testing.T, node *clusterNode) string {
	t.Helper()
	data, err := os.ReadFile(node.logPath)
	if err != nil {
		t.Fatalf("read %s log: %v", node.id, err)
	}
	return string(data)
}
