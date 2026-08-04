// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package migration

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	goblintransport "github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Production clients for migration (GOBLIN-DIV-031).
//
// The request/response messages (NodeCheckpointAgentInstanceRequest,
// NodeRestoreAgentInstanceRequest, NodePullCheckpointRequest and their
// responses) live in proto/goblin/v1/node_rpc.proto rather than as Go
// structs here (GOBLIN-DIV-036, batch D): the caller (this file) and
// the handler (internal/supervisor) both import goblinv1, so the
// shared-definition property the previous hand-written structs existed
// for is now buf's job, not this package's. The reconciler's
// StartAgentInstance call used to do the opposite - an anonymous struct
// matched by hand against a type in another package - and its own
// comments recorded how uncomfortable that was; it converts to the
// same messages in core/scheduler/reconciler.go.

// RPC method names. Constants because a typo in a method string fails
// at runtime on a remote node, which is the worst place to discover it.
const (
	MethodCheckpoint = "NodeRPC.CheckpointAgentInstance"
	MethodRestore    = "NodeRPC.RestoreAgentInstance"
	MethodPull       = "NodeRPC.PullCheckpoint"
	MethodReady      = "NodeRPC.MigrationReady"
)

// Caller is the RPC surface the migration clients need. It mirrors the
// scheduler's RPCClient; declared here so this package does not import
// the scheduler and create a cycle. Call takes proto.Message rather
// than internal/supervisor's own types for the same reason: importing
// internal/supervisor from core/migration would itself be a cycle
// (internal/supervisor already imports core/migration), so the
// dependency both sides can share without cycling is
// google.golang.org/protobuf/proto plus the generated goblinv1
// messages, not either domain package.
type Caller interface {
	Call(method string, req, resp proto.Message) error
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

func (r *RPCNodes) call(ctx context.Context, nodeID, method string, req, resp proto.Message) error {
	addr, err := r.resolve(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("resolving node %s: %w", nodeID, err)
	}
	client, err := r.dial(addr)
	if err != nil {
		return fmt.Errorf("dialing node %s at %s: %w", nodeID, addr, err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Call(method, req, resp); err != nil {
		return fmt.Errorf("%s on node %s: %w", method, nodeID, err)
	}
	return nil
}

// Checkpoint asks nodeID to dump the instance.
func (r *RPCNodes) Checkpoint(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64) error {
	req := &goblinv1.NodeCheckpointAgentInstanceRequest{
		InstanceId: instanceID, InstanceUuid: uuid, Epoch: epoch,
	}
	var resp goblinv1.NodeCheckpointAgentInstanceResponse
	return r.call(ctx, nodeID, MethodCheckpoint, req, &resp)
}

// Ready asks nodeID whether it can accept a migration, before anything
// is done to the source (GOBLIN-DIV-048). A node that is unreachable
// and a node that answers "not ready" are different failures and are
// reported as such: the first is an error from the call, the second a
// populated response the caller turns into a refusal.
func (r *RPCNodes) Ready(ctx context.Context, nodeID, instanceID string) (string, bool, error) {
	req := &goblinv1.NodeMigrationReadyRequest{InstanceId: instanceID}
	var resp goblinv1.NodeMigrationReadyResponse
	if err := r.call(ctx, nodeID, MethodReady, req, &resp); err != nil {
		return "", false, err
	}
	return resp.GetReason(), resp.GetReady(), nil
}

// Restore asks nodeID to restore the instance from its local image.
func (r *RPCNodes) Restore(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64, spec *goblinv1.AgentSpec) error {
	req := &goblinv1.NodeRestoreAgentInstanceRequest{
		InstanceId: instanceID, InstanceUuid: uuid, Epoch: epoch, Spec: spec,
	}
	var resp goblinv1.NodeRestoreAgentInstanceResponse
	return r.call(ctx, nodeID, MethodRestore, req, &resp)
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
	req := &goblinv1.NodePullCheckpointRequest{
		// InstanceId travels for diagnostics only - the transfer is
		// keyed by {uuid, epoch} - but omitting it produced log lines
		// reading "pulling image for  from ..." during the two-node
		// bring-up, which is precisely when they get read.
		InstanceId:   ident.String(uuid),
		InstanceUuid: uuid,
		Epoch:        epoch,
		SourceAddr:   sourceAddr,
		Token:        token,
	}
	var resp goblinv1.NodePullCheckpointResponse
	return p.nodes.call(ctx, destNodeID, MethodPull, req, &resp)
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
