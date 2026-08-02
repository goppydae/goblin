package scheduler

import (
	"context"

	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
	"google.golang.org/protobuf/proto"
)

// RPCClient defines a generic RPC client. Call carries a protobuf
// payload so buf breaking covers every field that crosses the wire
// (GOBLIN-DIV-036).
type RPCClient interface {
	Call(method string, req, resp proto.Message) error
	Close() error
}

// ClientFactory creates an RPC client for a given address
type ClientFactory func(addr string) (RPCClient, error)

// KVStore defines the interface for storage operations: the generic KV
// half plus the typed instance-lifecycle half (admission and
// transitions are Raft commands validated by the FSM, never plain
// writes).
type KVStore interface {
	Set(ctx context.Context, namespace, key string, value []byte) error
	Get(ctx context.Context, namespace, key string) ([]byte, bool, error)
	Scan(ctx context.Context, namespace, prefix string) (map[string][]byte, error)
	Delete(ctx context.Context, namespace, key string) error

	Admit(ctx context.Context, specUUID, instanceUUID []byte, nodeID string) error
	TransitionInstance(ctx context.Context, instanceUUID []byte, to goblinv1.InstanceState, reason string) error
	SignalInstance(ctx context.Context, req *goblinv1.SignalRequest) (string, error)
	GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, bool, error)
	ListInstances(ctx context.Context) ([]*goblinv1.AgentInstance, error)

	// MigrationInFlight reports whether a migration is under way for
	// this instance. The reconciler needs it because a migration STOPS
	// the source process on purpose, and a deliberate stop is
	// indistinguishable from a crash by heartbeat alone
	// (GOBLIN-DIV-049).
	MigrationInFlight(instanceUUID []byte) (*goblinv1.MigrationRecord, bool)
}

// Cluster defines the interface for cluster membership operations
type Cluster interface {
	Members() []serf.Member
}
