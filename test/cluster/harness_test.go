//go:build cluster

package main_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	gapitransport "github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/goblin/internal/cli"
	"github.com/goppydae/goblin/internal/ident"
	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// builtBinaries holds the once-per-run build outputs shared by every test.
var builtBinaries struct {
	goblind   string
	agentsDir string
}

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "goblin-cluster-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	builtBinaries.goblind = filepath.Join(tmp, "goblind")
	builtBinaries.agentsDir = filepath.Join(tmp, "agents")
	if err := os.MkdirAll(builtBinaries.agentsDir, 0750); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir agents:", err)
		os.Exit(1)
	}

	// One build for the daemon, one for the fixture agent; both from the
	// module so the e2e always tests the working tree.
	for _, b := range []struct{ out, pkg string }{
		{builtBinaries.goblind, "../../cmd/goblind"},
		{filepath.Join(builtBinaries.agentsDir, "sleeper"), "./fixtures/sleeper"},
	} {
		cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n", b.pkg, err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

// clusterNode is one goblind process under harness control.
type clusterNode struct {
	id       string
	serfAddr string
	apiAddr  string
	cmd      *exec.Cmd
	logPath  string
}

type testCluster struct {
	t     *testing.T
	nodes map[string]*clusterNode
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
	addrs := freeAddrs(c.t, 3)
	node := &clusterNode{
		id:       id,
		serfAddr: addrs[0],
		apiAddr:  addrs[2],
		logPath:  nodeLogPath(id),
	}

	args := []string{
		"--id", id,
		"--serf-addr", node.serfAddr,
		"--raft-addr", addrs[1],
		"--api-addr", node.apiAddr,
		"--data", filepath.Join(c.t.TempDir(), id+"-raft"),
		"--log-format", "json",
		"--log-level", "debug",
	}
	if join != "" {
		args = append(args, "--join", join)
	}

	logFile, err := os.Create(node.logPath)
	if err != nil {
		c.t.Fatalf("create node log: %v", err)
	}

	cmd := exec.Command(builtBinaries.goblind, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// The embedded agent manager discovers the fixture dir exclusively;
	// binaries resolve from the harness build, never from /bin (lessons:
	// harness isolation + sandbox-masked FHS paths).
	cmd.Env = append(os.Environ(), "RUNTIME_AGENT_PATH="+builtBinaries.agentsDir)
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
		c.startNode(fmt.Sprintf("node-%d", i), first.serfAddr)
	}
	return c
}

// client opens an RPC client against one node (insecure: dev cluster).
func (c *testCluster) client(node *clusterNode) *cli.QUICRPCClient {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		cl, err := cli.NewQUICRPCClient(node.apiAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
		if err == nil {
			return cl
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("connect to %s (%s): %v", node.id, node.apiAddr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// members calls SchedulerRPC.Members via the given node.
func (c *testCluster) members(node *clusterNode) ([]supervisor.MemberInfo, error) {
	cl, err := cli.NewQUICRPCClient(node.apiAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close members client: %v", cerr)
		}
	}()
	var members []supervisor.MemberInfo
	if err := cl.Call("SchedulerRPC.Members", struct{}{}, &members); err != nil {
		return nil, err
	}
	return members, nil
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
	cl, err := cli.NewQUICRPCClient(node.apiAddr, gapitransport.TLSConfig{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := cl.Close(); cerr != nil {
			c.t.Logf("close instances client: %v", cerr)
		}
	}()
	req := supervisor.ListAgentInstancesRequest{SpecID: specID}
	var out []*goblinv1.AgentInstance
	if err := cl.Call("SchedulerRPC.ListAgentInstances", &req, &out); err != nil {
		return nil, err
	}
	return out, nil
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
	req := supervisor.SignalAgentInstanceRequest{InstanceID: instanceID, Signum: signum}
	var resp string
	return cl.Call("SchedulerRPC.SignalAgentInstance", &req, &resp)
}
