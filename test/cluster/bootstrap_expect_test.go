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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

// TestBootstrapExpectMismatchAtLeastOneExitsNonzero pins the fail-loud
// property Task 5 introduced: a node that detects a bootstrap-expect
// disagreement in the seed set must exit nonzero, not merely stop
// (internal/supervisor/supervisor.go's failFatal path, reached through
// runBootstrapExpect's mismatch error in bootstrap.go).
//
// PlanBootstrap's mismatch check runs independently and symmetrically
// on each node once gossip has shown it the peer's differing
// bootstrap_expect tag, so which node detects the disagreement first
// is genuinely unspecified: a node can only detect it after gossip
// converges, and whichever side loses that race can die before the
// other side's own join round-trip completes. An earlier version of
// this test asserted a SPECIFIC node exits nonzero and was flaky (2
// passes, 5 timeouts, out of 7 runs) for exactly that reason. The
// correct, weaker claim is that AT LEAST ONE of the two exits nonzero
// within a generous bound - both exiting is fine, either is a pass,
// and neither exiting within the bound is the only failure.
//
// Both nodes are started directly with exec.CommandContext, not
// through the harness, so both process handles can be waited on and
// their exit codes inspected independently. Both get --operator-key so
// an unrelated keyless refusal can never be mistaken for the mismatch
// this test targets. cmd.Cancel is overridden to kill the whole
// process group (matching killNode's convention elsewhere in this
// package) so a context timeout cannot leave a node's children
// running; os/exec reports a signal-killed process as ExitCode() ==
// -1, distinct from a genuine os.Exit(1), so a timeout can never be
// misread as the pass this test is checking for.
func TestBootstrapExpectMismatchAtLeastOneExitsNonzero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	addrs := freeAddrs(t, 2)
	node1Addr, node2Addr := addrs[0], addrs[1]
	keyPath := operatorKeyPath(t)

	newCmd := func(id, addr, join, expect string) (*exec.Cmd, string) {
		logPath := filepath.Join(t.TempDir(), id+".log")
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatalf("create %s log: %v", id, err)
		}
		args := []string{
			"--id", id,
			"--listen-addr", addr,
			"--data", filepath.Join(t.TempDir(), id+"-raft"),
			"--log-format", "json",
			"--log-level", "debug",
			"--operator-key", keyPath,
			"--bootstrap-expect", expect,
		}
		if join != "" {
			args = append(args, "--join", join)
		}
		cmd := exec.CommandContext(ctx, builtBinaries.goblind, append([]string{"start"}, args...)...)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		cmd.Env = append(os.Environ(), agentPathEnv()...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return cmd, logPath
	}

	cmd1, logPath1 := newCmd("node-1", node1Addr, "", "2")
	cmd2, logPath2 := newCmd("node-2", node2Addr, node1Addr, "3")

	if err := cmd1.Start(); err != nil {
		t.Fatalf("start node-1: %v", err)
	}
	if err := cmd2.Start(); err != nil {
		_ = syscall.Kill(-cmd1.Process.Pid, syscall.SIGKILL)
		t.Fatalf("start node-2: %v", err)
	}

	type exitResult struct {
		id   string
		code int
	}
	results := make(chan exitResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	waitFor := func(id string, cmd *exec.Cmd) {
		defer wg.Done()
		_ = cmd.Wait()
		results <- exitResult{id: id, code: cmd.ProcessState.ExitCode()}
	}
	go waitFor("node-1", cmd1)
	go waitFor("node-2", cmd2)

	t.Cleanup(func() {
		if cmd1.Process != nil {
			_ = syscall.Kill(-cmd1.Process.Pid, syscall.SIGKILL)
		}
		if cmd2.Process != nil {
			_ = syscall.Kill(-cmd2.Process.Pid, syscall.SIGKILL)
		}
		wg.Wait()
		if t.Failed() {
			for id, path := range map[string]string{"node-1": logPath1, "node-2": logPath2} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Logf("read %s log: %v", id, err)
					continue
				}
				t.Logf("=== %s log ===\n%s", id, data)
			}
		}
	})

	var seen []exitResult
	sawNonzero := false
	for i := 0; i < 2; i++ {
		r := <-results
		seen = append(seen, r)
		t.Logf("%s exited with code %d", r.id, r.code)
		if r.code > 0 {
			sawNonzero = true
			break
		}
	}
	if !sawNonzero {
		t.Fatalf("neither node exited nonzero on a bootstrap-expect mismatch within the bound: %v", seen)
	}
}

func readNodeLog(t *testing.T, node *clusterNode) string {
	t.Helper()
	data, err := os.ReadFile(node.logPath)
	if err != nil {
		t.Fatalf("read %s log: %v", node.id, err)
	}
	return string(data)
}
