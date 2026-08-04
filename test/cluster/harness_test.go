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
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapiproduct "github.com/goppydae/gapi/core/product"
	gapitransport "github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/goblin/internal/cli"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
)

// builtBinaries holds the once-per-run build outputs shared by every test.
var builtBinaries struct {
	goblind   string
	agentsDir string
	// scratch is the run-scoped temp dir every other per-run artifact
	// lives under, so one removal in runTests cleans all of them.
	scratch string
}

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

// runTests is TestMain's body pulled into a function so its defers
// actually run. os.Exit skips defers, so a cleanup deferred in TestMain
// itself never fires and the run's scratch dir leaks once per e2e
// binary invocation.
func runTests(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "goblin-cluster-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		return 1
	}
	defer func() {
		if rerr := os.RemoveAll(tmp); rerr != nil {
			fmt.Fprintln(os.Stderr, "remove scratch dir:", rerr)
		}
	}()

	builtBinaries.scratch = tmp
	builtBinaries.goblind = filepath.Join(tmp, "goblind")
	builtBinaries.agentsDir = filepath.Join(tmp, "agents")
	if err := os.MkdirAll(builtBinaries.agentsDir, 0750); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir agents:", err)
		return 1
	}

	// One build for the daemon, one for the fixture agent; both from the
	// module so the e2e always tests the working tree.
	//
	// GOBLIN_TEST_BIN_DIR overrides the build with binaries staged
	// beforehand. The VM checks need it: a NixOS guest running the CRIU
	// suite has no Go toolchain and no module cache, and building inside
	// the guest would test whatever the guest could compile rather than
	// what the derivation was built from.
	if staged := os.Getenv("GOBLIN_TEST_BIN_DIR"); staged != "" {
		builtBinaries.goblind = filepath.Join(staged, "goblind")
		builtBinaries.agentsDir = filepath.Join(staged, "agents")
		if _, serr := os.Stat(builtBinaries.goblind); serr != nil {
			fmt.Fprintf(os.Stderr, "GOBLIN_TEST_BIN_DIR set but %s is missing: %v\n",
				builtBinaries.goblind, serr)
			return 1
		}
	} else {
		for _, b := range []struct{ out, pkg string }{
			{builtBinaries.goblind, "../../cmd/goblind"},
			{filepath.Join(builtBinaries.agentsDir, "sleeper"), "./fixtures/sleeper"},
		} {
			cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "build %s: %v\n", b.pkg, err)
				return 1
			}
		}
	}

	return m.Run()
}

// clusterNode is one goblind process under harness control.
type clusterNode struct {
	id string
	// listenAddr is the node's single control-plane address: gossip,
	// raft, RPC, and kernel traffic all ride it, routed by ALPN.
	listenAddr string
	cmd        *exec.Cmd
	logPath    string
}

type testCluster struct {
	t     *testing.T
	nodes map[string]*clusterNode

	// noOperatorKey starts nodes without --operator-key, leaving the
	// registry empty. Only the fail-closed test sets it; every other
	// path supplies a key so mutations are authorized.
	noOperatorKey bool
}

// operatorKeyPath writes a throwaway operator public key once per test
// binary and returns its path. Every node in the harness is started
// with it, so the e2e cluster seeds a registry and mutations are
// authorized; the keyless case is exercised deliberately in
// operator_keys_test.go instead.
var (
	operatorKeyOnce sync.Once
	operatorKeyFile string
	operatorKeyErr  error
)

func operatorKeyPath(t *testing.T) string {
	t.Helper()
	operatorKeyOnce.Do(func() {
		var kp *gapicrypto.KeyPair
		kp, operatorKeyErr = gapicrypto.GenerateKey()
		if operatorKeyErr != nil {
			return
		}
		// The key lives in the run scratch dir rather than its own
		// MkdirTemp. It is created lazily inside a sync.Once shared by
		// every test, so t.Cleanup on whichever test happened to call
		// first would delete it while later tests still need it;
		// runTests' removal is the only hook whose lifetime matches the
		// key's.
		operatorKeyFile = filepath.Join(builtBinaries.scratch, "operator.pub")
		operatorKeyErr = kp.SavePublic(operatorKeyFile)
	})
	if operatorKeyErr != nil {
		t.Fatalf("prepare operator key: %v", operatorKeyErr)
	}
	return operatorKeyFile
}

// freeAddrs reserves n distinct loopback ports and returns them as
// host:port strings. The listeners are closed just before use; loopback
// reuse in the gap is effectively immediate on Linux.
func freeAddrs(t *testing.T, n int) []string {
	t.Helper()
	addrs := make([]string, 0, n)
	listeners := make([]net.Listener, 0, n)
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve port: %v", err)
		}
		listeners = append(listeners, ln)
		addrs = append(addrs, ln.Addr().String())
	}
	for _, ln := range listeners {
		if err := ln.Close(); err != nil {
			t.Fatalf("release port: %v", err)
		}
	}
	return addrs
}

// startNode launches a goblind process. join is empty for the first node.
func (c *testCluster) startNode(id, join string) *clusterNode {
	c.t.Helper()
	return c.startNodeWithArgs(id, join)
}

// startNodeWithArgs is startNode plus extra goblind flags, for
// scenarios that vary the daemon's configuration (bootstrap-expect).
func (c *testCluster) startNodeWithArgs(id, join string, extra ...string) *clusterNode {
	c.t.Helper()
	addrs := freeAddrs(c.t, 1)
	node := &clusterNode{
		id:         id,
		listenAddr: addrs[0],
		logPath:    nodeLogPath(id),
	}

	args := []string{
		"--id", id,
		"--listen-addr", node.listenAddr,
		"--data", filepath.Join(c.t.TempDir(), id+"-raft"),
		"--log-format", "json",
		"--log-level", "debug",
	}
	if join != "" {
		args = append(args, "--join", join)
	}
	if !c.noOperatorKey {
		args = append(args, "--operator-key", operatorKeyPath(c.t))
	}
	args = append(args, extra...)

	logFile, err := os.Create(node.logPath)
	if err != nil {
		c.t.Fatalf("create node log: %v", err)
	}

	cmd := exec.Command(builtBinaries.goblind, append([]string{"start"}, args...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// The embedded agent manager discovers the fixture dir exclusively;
	// binaries resolve from the harness build, never from /bin (lessons:
	// harness isolation + sandbox-masked FHS paths).
	cmd.Env = append(os.Environ(), agentPathEnv()...)
	// Process-group kill: instance processes spawned by the node must die
	// with it, or go test's I/O watchdog hangs on inherited pipes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		c.t.Fatalf("start %s: %v", id, err)
	}
	node.cmd = cmd
	c.nodes[id] = node
	c.t.Cleanup(func() {
		c.killNode(node)
		if c.t.Failed() {
			c.dumpLog(node)
		}
	})
	return node
}

// nodeLogPath keeps node logs outside t.TempDir so failed runs leave
// evidence behind; GOBLIN_E2E_LOGDIR overrides the default temp dir.
func nodeLogPath(id string) string {
	dir := os.Getenv("GOBLIN_E2E_LOGDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "goblin-e2e-"+id+".log")
}

// dumpLog prints the tail of a node's log so failures carry evidence.
func (c *testCluster) dumpLog(node *clusterNode) {
	data, err := os.ReadFile(node.logPath)
	if err != nil {
		c.t.Logf("read %s log: %v", node.id, err)
		return
	}
	const tail = 40000
	if len(data) > tail {
		data = data[len(data)-tail:]
	}
	c.t.Logf("=== %s log tail ===\n%s", node.id, data)
}

// killNode SIGKILLs the node's whole process group; idempotent.
func (c *testCluster) killNode(node *clusterNode) {
	if node.cmd == nil || node.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-node.cmd.Process.Pid, syscall.SIGKILL)
	_ = node.cmd.Wait()
	node.cmd = nil
	delete(c.nodes, node.id)
}

// startCluster boots n nodes joined through the first one.
func startCluster(t *testing.T, n int) *testCluster {
	t.Helper()
	c := &testCluster{t: t, nodes: map[string]*clusterNode{}}
	first := c.startNode("node-1", "")
	for i := 2; i <= n; i++ {
		c.startNode(fmt.Sprintf("node-%d", i), first.listenAddr)
	}
	return c
}

// startKeylessNode brings up a single node with no operator key, so its
// registry stays empty. It exists only for the fail-closed test.
func startKeylessNode(t *testing.T, id string) (*testCluster, *clusterNode) {
	t.Helper()
	c := &testCluster{t: t, nodes: map[string]*clusterNode{}, noOperatorKey: true}
	return c, c.startNode(id, "")
}

// client opens an RPC client against one node (insecure: dev cluster).
func (c *testCluster) client(node *clusterNode) *cli.QUICRPCClient {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cl, err := cli.NewQUICRPCClient(node.listenAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
		if err == nil {
			return cl
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("connect to %s (%s): %v", node.id, node.listenAddr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// members calls SchedulerRPC.Members via the given node.
func (c *testCluster) members(node *clusterNode) ([]*goblinv1.MemberInfo, error) {
	cl, err := cli.NewQUICRPCClient(node.listenAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close members client: %v", cerr)
		}
	}()
	var resp goblinv1.MembersResponse
	if err := cl.Call("SchedulerRPC.Members", &goblinv1.MembersRequest{}, &resp); err != nil {
		return nil, err
	}
	return resp.GetMembers(), nil
}

// waitLeader polls until the cluster (as seen from `via`) has n alive
// members and exactly one leader; returns the leader's node.
func (c *testCluster) waitLeader(via *clusterNode, n int, timeout time.Duration) *clusterNode {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		members, err := c.members(via)
		if err == nil && len(members) >= n {
			var leaderName string
			leaders := 0
			for _, m := range members {
				if m.Leader {
					leaders++
					leaderName = m.Name
				}
			}
			if leaders == 1 {
				if node, ok := c.nodes[leaderName]; ok {
					return node
				}
			}
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("no single leader among %d members within %s (last err: %v)", n, timeout, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// instances lists instance records for a spec via the given node.
func (c *testCluster) instances(node *clusterNode, specID string) ([]*goblinv1.AgentInstance, error) {
	cl, err := cli.NewQUICRPCClient(node.listenAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close instances client: %v", cerr)
		}
	}()
	req := &goblinv1.ListAgentInstancesRequest{SpecId: specID}
	var resp goblinv1.ListAgentInstancesResponse
	if err := cl.Call("SchedulerRPC.ListAgentInstances", req, &resp); err != nil {
		return nil, err
	}
	return resp.GetInstances(), nil
}

// waitInstances polls until the spec has exactly `count` running
// instances; returns them and the elapsed time.
func (c *testCluster) waitInstances(via *clusterNode, specID string, count int, timeout time.Duration) ([]*goblinv1.AgentInstance, time.Duration) {
	c.t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	var last []*goblinv1.AgentInstance
	for {
		insts, err := c.instances(via, specID)
		if err == nil {
			running := insts[:0:0]
			for _, in := range insts {
				if in.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
					running = append(running, in)
				}
			}
			last = running
			if len(running) == count && len(insts) == count {
				return running, time.Since(start)
			}
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("spec %s: wanted %d running instances within %s, have %v (err: %v)",
				specID, count, timeout, describeInstances(last), err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func describeInstances(insts []*goblinv1.AgentInstance) []string {
	out := make([]string, 0, len(insts))
	for _, in := range insts {
		out = append(out, fmt.Sprintf("%s@%s=%s", ident.String(in.InstanceUuid), in.NodeId, in.State))
	}
	return out
}

// uuidStr renders instance UUID bytes canonically for test assertions.
func uuidStr(b []byte) string { return ident.String(b) }

// signal delivers a signal to an instance through the leader, failing
// the test on refusal.
func (c *testCluster) signal(node *clusterNode, instanceID string, signum int32) {
	c.t.Helper()
	if err := c.trySignal(node, instanceID, signum); err != nil {
		c.t.Fatalf("signal %s with %d: %v", instanceID, signum, err)
	}
}

// trySignal delivers a signal and returns the RPC error, for scenarios
// asserting refusal.
func (c *testCluster) trySignal(node *clusterNode, instanceID string, signum int32) error {
	cl := c.client(node)
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close signal client: %v", cerr)
		}
	}()
	req := &goblinv1.SignalAgentInstanceRequest{InstanceId: instanceID, Signum: signum}
	var resp goblinv1.SignalAgentInstanceResponse
	return cl.Call("SchedulerRPC.SignalAgentInstance", req, &resp)
}

// dialALPNAddr probes one ALPN plane of a node's single control-plane
// address (the GOBLIN-DIV-023 mechanical proof).
func dialALPNAddr(addr, alpn string) (*quic.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true, // harness nodes run ephemeral dev certs
		NextProtos:         []string{alpn},
	}, &quic.Config{EnableDatagrams: true})
}

// agentPathEnv is the environment assignment that fences a spawned
// goblind's agent discovery to the harness fixture directory.
//
// It is COMPOSED through the kernel's own registry rather than spelled
// as a literal, and that is the whole point. The variable carried the
// kernel's pre-GAPI-DIV-059 namespace; that entry renamed it and
// GAPI-DIV-061 made it derive from the product, so a hand-written name
// here could disagree with the name the embedded kernel actually reads -
// and the disagreement is SILENT. Discovery would simply stop being
// fenced, fall back to the default search path, find no fixtures, and
// the cluster tests would fail as a placement TIMEOUT: the same shape as
// goblin's known intermittent main flake, and therefore the hardest
// possible thing to attribute correctly.
//
// AGENT_PATH ALONE NO LONGER FENCES. Up to gapi v0.1.0-proto2e it
// REPLACED the search path, so naming a directory fenced discovery as a
// side effect. proto2f made it additive - it PREPENDS - and moved the
// fence behind AGENT_PATH_EXCLUSIVE, which is why both are set here.
// The new failure shape is the opposite of the old one and must not be
// hunted for as a timeout: the fixtures are still found, and found
// first, so the suite PASSES while discovery quietly also scans the
// user-scope tiers. It fails OPEN. gapi hit the real cost of that in
// GAPI-DIV-021, where the checkout's own agents starved a fixture's
// state transitions.
//
// The child is goblind, so its product is "goblin"; the harness knows
// that because it built the child. Setting the identity in this process
// is what lets EnvKey compose, and it asserts nothing about this test
// binary beyond which binary it is about to run.
func agentPathEnv() []string {
	gapiproduct.Set("goblin")
	return []string{
		gapiproduct.EnvKey("AGENT_PATH") + "=" + builtBinaries.agentsDir,
		gapiproduct.EnvKey("AGENT_PATH_EXCLUSIVE") + "=1",
	}
}
