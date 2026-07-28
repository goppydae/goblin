package migration

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"

	goblintransport "github.com/goppydae/goblin/core/transport"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Wire types and production clients for migration (GOBLIN-DIV-031).
//
// The request structs live here rather than in the supervisor so that
// the caller and the handler share one definition. The reconciler's
// StartAgentInstance call does the opposite - an anonymous struct
// matched by hand against a type in another package - and its own
// comments record how uncomfortable that is.

// CheckpointRPCRequest asks a node to dump one instance it is running.
type CheckpointRPCRequest struct {
	InstanceID   string
	InstanceUUID []byte
	Epoch        uint64
}

// RestoreRPCRequest asks a node to restore an instance from an image
// already present in its own store.
type RestoreRPCRequest struct {
	InstanceID   string
	InstanceUUID []byte
	Epoch        uint64
	Spec         *goblinv1.AgentSpec
}

// PullRPCRequest asks a node to fetch an image from a peer.
//
// The DESTINATION receives this and does the dialing: the coordinator
// runs on the leader, which is frequently neither end of the transfer,
// and routing bytes through it would put a multi-gigabyte stream on the
// node running consensus.
type PullRPCRequest struct {
	InstanceID   string
	InstanceUUID []byte
	Epoch        uint64
	SourceAddr   string
	Token        []byte
}

// RPC method names. Constants because a typo in a method string fails
// at runtime on a remote node, which is the worst place to discover it.
const (
	MethodCheckpoint = "NodeRPC.CheckpointAgentInstance"
	MethodRestore    = "NodeRPC.RestoreAgentInstance"
	MethodPull       = "NodeRPC.PullCheckpoint"
)

// Caller is the RPC surface the migration clients need. It mirrors the
// scheduler's RPCClient; declared here so this package does not import
// the scheduler and create a cycle.
type Caller interface {
	Call(serviceMethod string, args interface{}, reply interface{}) error
	Close() error
}

// Dialer opens an RPC client to a node address.
type Dialer func(addr string) (Caller, error)

// Resolver maps a node id to its control-plane address.
type Resolver func(ctx context.Context, nodeID string) (string, error)

// RPCNodes drives the node-local checkpoint and restore RPCs. It
// satisfies NodeClient.
type RPCNodes struct {
	dial    Dialer
	resolve Resolver
}

// NewRPCNodes wires a production NodeClient.
func NewRPCNodes(dial Dialer, resolve Resolver) *RPCNodes {
	return &RPCNodes{dial: dial, resolve: resolve}
}

func (r *RPCNodes) call(ctx context.Context, nodeID, method string, req interface{}) error {
	addr, err := r.resolve(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("resolving node %s: %w", nodeID, err)
	}
	client, err := r.dial(addr)
	if err != nil {
		return fmt.Errorf("dialing node %s at %s: %w", nodeID, addr, err)
	}
	defer func() { _ = client.Close() }()

	var resp string
	if err := client.Call(method, req, &resp); err != nil {
		return fmt.Errorf("%s on node %s: %w", method, nodeID, err)
	}
	return nil
}

// Checkpoint asks nodeID to dump the instance.
func (r *RPCNodes) Checkpoint(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64) error {
	return r.call(ctx, nodeID, MethodCheckpoint, &CheckpointRPCRequest{
		InstanceID: instanceID, InstanceUUID: uuid, Epoch: epoch,
	})
}

// Restore asks nodeID to restore the instance from its local image.
func (r *RPCNodes) Restore(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64, spec *goblinv1.AgentSpec) error {
	return r.call(ctx, nodeID, MethodRestore, &RestoreRPCRequest{
		InstanceID: instanceID, InstanceUUID: uuid, Epoch: epoch, Spec: spec,
	})
}

// RPCPuller tells the destination to pull an image from the source. It
// satisfies ImagePuller.
type RPCPuller struct {
	nodes *RPCNodes
}

// NewRPCPuller wires a production ImagePuller over the same dialer.
func NewRPCPuller(nodes *RPCNodes) *RPCPuller { return &RPCPuller{nodes: nodes} }

// Pull resolves the source's address and hands it to the destination,
// which does the fetching.
func (p *RPCPuller) Pull(ctx context.Context, destNodeID, sourceNodeID string, uuid []byte, epoch uint64, token []byte) error {
	sourceAddr, err := p.nodes.resolve(ctx, sourceNodeID)
	if err != nil {
		return fmt.Errorf("resolving source node %s: %w", sourceNodeID, err)
	}
	return p.nodes.call(ctx, destNodeID, MethodPull, &PullRPCRequest{
		InstanceUUID: uuid,
		Epoch:        epoch,
		SourceAddr:   sourceAddr,
		Token:        token,
	})
}

// DialAndFetch dials a peer's goblin-ckpt listener and pulls one image
// into store, returning the local directory.
//
// The TLS config is supplied by the caller rather than built here: this
// package must not decide the cluster's verification policy, and a
// default that skipped verification would be a memory-image disclosure
// waiting to happen.
func DialAndFetch(ctx context.Context, sourceAddr string, tlsConf *tls.Config, store *Store, uuid []byte, epoch uint64, token []byte) (string, error) {
	if tlsConf == nil {
		return "", fmt.Errorf("migration: refusing to fetch without a TLS configuration")
	}
	// Copy before mutating: the caller's config is likely shared with
	// the other planes, and stamping our ALPN onto it would redirect
	// their dials.
	conf := tlsConf.Clone()
	conf.NextProtos = []string{goblintransport.ALPNGoblinCkpt}

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, sourceAddr, conf, &quic.Config{
		MaxIdleTimeout:  5 * time.Minute,
		KeepAlivePeriod: 15 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("dialing %s for checkpoint image: %w", sourceAddr, err)
	}
	defer func() { _ = conn.CloseWithError(0, "fetch complete") }()

	return NewClient(store).Fetch(ctx, conn, uuid, epoch, token)
}
