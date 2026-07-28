package supervisor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/core/procsig"

	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/logattr"
)

// Node-local halves of a migration (GOBLIN-DIV-018).
//
// The source dumps into its image store; the destination pulls the
// image (core/migration) and restores from it. Neither side decides
// that a migration should happen - that is the leader's MIGRATE_BEGIN -
// and neither moves the instance's Raft record. All they change is
// where the process runs, which the heartbeat publishes as a locator.

// Request types are shared with the callers in core/migration rather
// than redeclared here, so a field added on one side cannot silently
// go unread on the other.
type (
	CheckpointAgentRequest = migration.CheckpointRPCRequest
	RestoreAgentRequest    = migration.RestoreRPCRequest
	PullCheckpointRequest  = migration.PullRPCRequest
)

// CheckpointAgentInstance dumps a running instance into this node's
// image store, leaving it stopped.
//
// Stopped is the point. The image is the rollback artifact for the
// migration, and a source that kept running past the state its image
// captured would have diverged from it before the destination even
// started restoring.
func (n *NodeRPC) CheckpointAgentInstance(req *CheckpointAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}

	a := n.agentMgr.Get(req.InstanceID)
	if a == nil {
		return fmt.Errorf("instance %s is not running on this node", req.InstanceID)
	}
	ckpt, ok := a.(lifecycle.Checkpointer)
	if !ok {
		// Declining the capability is a legitimate answer, not a bug:
		// an in-process runner has nothing for CRIU to dump. The
		// orchestrator should have asserted this at admission, so
		// reaching here means the assertion was skipped.
		return fmt.Errorf("instance %s runs an agent type that cannot be checkpointed", req.InstanceID)
	}

	dir, err := n.images.Create(req.InstanceUUID, req.Epoch)
	if err != nil {
		return fmt.Errorf("checkpoint instance %s: %w", req.InstanceID, err)
	}
	if err := ckpt.Checkpoint(context.Background(), dir); err != nil {
		return fmt.Errorf("checkpoint instance %s: %w", req.InstanceID, err)
	}

	// The process is stopped; its locator is no longer valid here. Zero
	// the identity so this node stops publishing a pid that a signal
	// could still be routed to.
	n.tracker.SetIdentity(req.InstanceID, 0, 0)
	n.tracker.Set(req.InstanceID, "checkpointed")

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance checkpointed",
		logattr.InstanceID(req.InstanceID), slog.String("dir", dir), slog.Uint64("epoch", req.Epoch))
	*resp = dir
	return nil
}

// RestoreAgentInstance restores an instance from an image in this
// node's store and adopts the resulting process.
//
// The identity captured afterwards is what makes the migration visible
// to the rest of the cluster: the restored process has a new pid, a new
// pid namespace inode and a new start epoch, and publishing them
// through the ordinary heartbeat is the locator move. The instance UUID
// does not change - that is the entire migration semantic.
func (n *NodeRPC) RestoreAgentInstance(req *RestoreAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}
	if req.Spec == nil {
		return fmt.Errorf("restore request for %s carries no spec", req.InstanceID)
	}

	dir, err := n.images.Dir(req.InstanceUUID, req.Epoch)
	if err != nil {
		return fmt.Errorf("restore instance %s: %w", req.InstanceID, err)
	}

	a, err := n.agentMgr.Instantiate(req.InstanceID, req.Spec.Type)
	if err != nil {
		return fmt.Errorf("instantiate %q as %s: %w", req.Spec.Type, req.InstanceID, err)
	}
	ckpt, ok := a.(lifecycle.Checkpointer)
	if !ok {
		n.agentMgr.Deregister(req.InstanceID)
		return fmt.Errorf("agent type %q cannot be restored from a checkpoint", req.Spec.Type)
	}
	if err := ckpt.Restore(context.Background(), dir); err != nil {
		n.agentMgr.Deregister(req.InstanceID)
		return fmt.Errorf("restore instance %s: %w", req.InstanceID, err)
	}

	n.tracker.Set(req.InstanceID, "running")
	n.captureIdentity(req.InstanceID, a)

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance restored",
		logattr.InstanceID(req.InstanceID), slog.String("dir", dir), slog.Uint64("epoch", req.Epoch))
	*resp = fmt.Sprintf("instance %s restored on node", req.InstanceID)
	return nil
}

// captureIdentity records the process identity behind an instance, the
// same way the start path does. Factored out because restore must
// produce byte-for-byte the same locator shape as a fresh start; a
// second, subtly different implementation is how the two paths would
// drift.
func (n *NodeRPC) captureIdentity(instanceID string, a any) {
	p, ok := a.(interface{ Pid() (int, bool) })
	if !ok {
		return
	}
	pid, running := p.Pid()
	if !running {
		return
	}
	if pi, err := procsig.Identify(pid); err == nil {
		n.tracker.SetIdentity(instanceID, pi.Pid, pi.StartEpoch)
		return
	}
	// Identify failed: record the pid with a zero epoch rather than
	// nothing. A zero epoch fails the delivery guard closed, which is
	// the safe direction - better to refuse a signal than to aim one at
	// an unverified pid.
	n.tracker.SetIdentity(instanceID, pid, 0)
}

// PullCheckpoint fetches an instance's image from a peer into this
// node's store.
//
// This runs on the DESTINATION. The coordinator on the leader tells it
// where to pull from rather than relaying the bytes: the leader is
// frequently neither end of the transfer, and routing a multi-gigabyte
// image through the node running consensus is exactly what the separate
// goblin-ckpt ALPN exists to avoid.
func (n *NodeRPC) PullCheckpoint(req *PullCheckpointRequest, resp *string) error {
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}
	if n.ckptTLS == nil {
		return fmt.Errorf("checkpoint transport TLS not configured on this node")
	}
	if req.SourceAddr == "" {
		return fmt.Errorf("pull request for %s names no source address", req.InstanceID)
	}

	dir, err := migration.DialAndFetch(context.Background(), req.SourceAddr, n.ckptTLS,
		n.images, req.InstanceUUID, req.Epoch, req.Token)
	if err != nil {
		return fmt.Errorf("pulling image for %s from %s: %w", req.InstanceID, req.SourceAddr, err)
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "checkpoint image pulled",
		logattr.InstanceID(req.InstanceID), slog.String("source", req.SourceAddr),
		slog.String("dir", dir), slog.Uint64("epoch", req.Epoch))
	*resp = dir
	return nil
}

// Images is this node's checkpoint image store.
func (n *NodeRPC) Images() *migration.Store { return n.images }
