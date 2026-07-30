package migration_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/migration"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// recordingCaller captures what actually went on the wire. The method
// names and payload shapes are the contract between the coordinator and
// the handlers registered on every node; a mismatch fails at runtime on
// a remote machine, which is the worst place to find it.
type recordingCaller struct {
	method string
	args   proto.Message
	err    error
	closed bool
}

func (c *recordingCaller) Call(method string, req, _ proto.Message) error {
	c.method = method
	c.args = req
	return c.err
}

func (c *recordingCaller) Close() error { c.closed = true; return nil }

func fixedResolver(addr string) migration.Resolver {
	return func(_ context.Context, _ string) (string, error) { return addr, nil }
}

var rpcUUID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

func TestCheckpointUsesRegisteredMethodName(t *testing.T) {
	caller := &recordingCaller{}
	nodes := migration.NewRPCNodes(
		func(string) (migration.Caller, error) { return caller, nil },
		fixedResolver("10.0.0.1:7946"),
	)

	if err := nodes.Checkpoint(context.Background(), "node-1", "inst-1", rpcUUID, 3); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if caller.method != migration.MethodCheckpoint {
		t.Errorf("method = %q, want %q", caller.method, migration.MethodCheckpoint)
	}
	req, ok := caller.args.(*goblinv1.NodeCheckpointAgentInstanceRequest)
	if !ok {
		t.Fatalf("args = %T, want *goblinv1.NodeCheckpointAgentInstanceRequest", caller.args)
	}
	if req.GetInstanceId() != "inst-1" || req.GetEpoch() != 3 {
		t.Errorf("request = %+v, want inst-1 at epoch 3", req)
	}
	if !caller.closed {
		t.Error("rpc client was not closed; migrations would leak a connection each")
	}
}

func TestRestoreCarriesTheSpec(t *testing.T) {
	caller := &recordingCaller{}
	nodes := migration.NewRPCNodes(
		func(string) (migration.Caller, error) { return caller, nil },
		fixedResolver("10.0.0.2:7946"),
	)
	spec := &goblinv1.AgentSpec{Type: "worker"}

	if err := nodes.Restore(context.Background(), "node-2", "inst-1", rpcUUID, 3, spec); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if caller.method != migration.MethodRestore {
		t.Errorf("method = %q, want %q", caller.method, migration.MethodRestore)
	}
	req := caller.args.(*goblinv1.NodeRestoreAgentInstanceRequest)
	if req.GetSpec().GetType() != "worker" {
		// Without the spec the destination cannot instantiate the agent
		// type it is about to restore into.
		t.Error("restore request dropped the spec")
	}
}

// The pull must be addressed to the DESTINATION and must carry the
// SOURCE's address. Getting these backwards would have the node being
// torn down fetch from the one taking over.
func TestPullTargetsDestinationWithSourceAddress(t *testing.T) {
	var dialed []string
	caller := &recordingCaller{}
	nodes := migration.NewRPCNodes(
		func(addr string) (migration.Caller, error) {
			dialed = append(dialed, addr)
			return caller, nil
		},
		func(_ context.Context, nodeID string) (string, error) {
			return map[string]string{
				"node-1": "10.0.0.1:7946",
				"node-2": "10.0.0.2:7946",
			}[nodeID], nil
		},
	)

	if err := migration.NewRPCPuller(nodes).
		Pull(context.Background(), "node-2", "node-1", rpcUUID, 4, []byte("tok")); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if caller.method != migration.MethodPull {
		t.Errorf("method = %q, want %q", caller.method, migration.MethodPull)
	}
	req := caller.args.(*goblinv1.NodePullCheckpointRequest)
	if req.GetSourceAddr() != "10.0.0.1:7946" {
		t.Errorf("SourceAddr = %q, want the source node's address", req.GetSourceAddr())
	}
	if len(dialed) == 0 || dialed[len(dialed)-1] != "10.0.0.2:7946" {
		t.Errorf("dialed %v, want the request sent to the destination", dialed)
	}
}

func TestResolverFailureIsReported(t *testing.T) {
	boom := errors.New("node not in membership")
	nodes := migration.NewRPCNodes(
		func(string) (migration.Caller, error) { return &recordingCaller{}, nil },
		func(context.Context, string) (string, error) { return "", boom },
	)

	err := nodes.Checkpoint(context.Background(), "ghost", "inst-1", rpcUUID, 1)
	if !errors.Is(err, boom) {
		t.Fatalf("want the resolver error, got %v", err)
	}
}

// Refusing without TLS is deliberate: the transfer carries an
// instance's memory image, so a default that skipped verification would
// be a disclosure waiting to happen.
func TestDialAndFetchRefusesWithoutTLS(t *testing.T) {
	store := migration.NewStore(t.TempDir())
	_, err := migration.DialAndFetch(context.Background(), "10.0.0.1:7946", nil, store, rpcUUID, 1, nil)
	if err == nil {
		t.Fatal("fetch proceeded with no TLS configuration")
	}
}
