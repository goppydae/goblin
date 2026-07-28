package migration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// Coordinator drives one migration from intent to outcome.
//
// The sequence is: commit the intent through Raft, dump on the source,
// pull the image to the destination, restore there, then commit the
// outcome. Every step before the final commit is reversible, and the
// image is what makes it so - which is why a dump stops the source
// rather than leaving it running.
//
// Each step is an interface rather than a concrete client so the
// failure paths are testable. Migration failures are rare in practice
// and catastrophic when mishandled, so they must be exercised
// deterministically rather than waited for.
type Coordinator struct {
	raft   Proposer
	nodes  NodeClient
	images ImagePuller
	log    *slog.Logger
}

// Proposer commits migration records through Raft. Implemented by the
// consensus layer; an interface here so the coordinator can be tested
// without standing up a quorum.
type Proposer interface {
	ProposeMigrateBegin(ctx context.Context, mb *goblinv1.MigrateBegin) error
	ProposeMigrateCommit(ctx context.Context, mc *goblinv1.MigrateCommit) error
}

// NodeClient reaches the node-local checkpoint and restore RPCs.
type NodeClient interface {
	Checkpoint(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64) error
	Restore(ctx context.Context, nodeID, instanceID string, uuid []byte, epoch uint64, spec *goblinv1.AgentSpec) error
}

// ImagePuller moves an image from the source node to the destination.
// The destination does the pulling; this is the coordinator asking it
// to, not the coordinator moving bytes itself.
type ImagePuller interface {
	Pull(ctx context.Context, destNodeID, sourceNodeID string, uuid []byte, epoch uint64, token []byte) error
}

// NewCoordinator wires a coordinator.
func NewCoordinator(raft Proposer, nodes NodeClient, images ImagePuller, log *slog.Logger) *Coordinator {
	if log == nil {
		log = slog.Default()
	}
	return &Coordinator{raft: raft, nodes: nodes, images: images, log: log}
}

// Request is one migration.
type Request struct {
	InstanceID   string
	InstanceUUID []byte
	SourceNode   string
	TargetNode   string
	Epoch        uint64
	Rights       uint64
	Token        []byte
	Spec         *goblinv1.AgentSpec
}

// ErrRolledBack wraps the original failure of a migration that was
// successfully undone. The instance is running on its source and the
// cluster is consistent; the caller may retry.
var ErrRolledBack = errors.New("migration rolled back")

// ErrStranded is returned when a migration failed AND the rollback also
// failed. The instance is running nowhere and needs an operator: it is
// deliberately distinct from ErrRolledBack, because treating the two
// the same is how a retry loop spins against a broken instance.
var ErrStranded = errors.New("migration failed and rollback failed; instance is not running")

// Migrate moves one instance and returns only after the outcome is
// committed.
//
// On any failure after the dump, the source is restored from the same
// image and the migration is committed as ABORTED. The instance's
// lifecycle state never changes and its UUID never moves - only its
// locator does, and only on success.
func (c *Coordinator) Migrate(ctx context.Context, req Request) error {
	if len(req.InstanceUUID) != uuidLen {
		return fmt.Errorf("%w: got %d bytes", ErrBadUUID, len(req.InstanceUUID))
	}
	if req.SourceNode == "" || req.TargetNode == "" {
		return fmt.Errorf("migration needs both a source and a target node")
	}
	if req.SourceNode == req.TargetNode {
		return fmt.Errorf("migration source and target are both %s", req.SourceNode)
	}

	// 1. Intent. This is also the concurrency gate: the FSM refuses a
	// second migration of the same instance, so a lost response cannot
	// produce two coordinators moving one process.
	if err := c.raft.ProposeMigrateBegin(ctx, &goblinv1.MigrateBegin{
		InstanceUuid:    req.InstanceUUID,
		TargetNodeId:    req.TargetNode,
		CheckpointEpoch: req.Epoch,
		Rights:          req.Rights,
	}); err != nil {
		// Nothing has happened to the process yet; no rollback needed
		// and no commit to write, since no begin was recorded.
		return fmt.Errorf("migration of %s: recording intent: %w", req.InstanceID, err)
	}

	// 2. Dump. After this the source process is STOPPED.
	if err := c.nodes.Checkpoint(ctx, req.SourceNode, req.InstanceID, req.InstanceUUID, req.Epoch); err != nil {
		// The source never stopped, so aborting is enough - there is
		// nothing to restore.
		c.abort(ctx, req, "checkpoint failed")
		return fmt.Errorf("migration of %s: checkpoint on %s: %w", req.InstanceID, req.SourceNode, err)
	}

	// 3. Transfer, then 4. restore. From here a failure means the
	// source is stopped and must be brought back.
	if err := c.images.Pull(ctx, req.TargetNode, req.SourceNode, req.InstanceUUID, req.Epoch, req.Token); err != nil {
		return c.rollback(ctx, req, fmt.Errorf("pulling image to %s: %w", req.TargetNode, err))
	}
	if err := c.nodes.Restore(ctx, req.TargetNode, req.InstanceID, req.InstanceUUID, req.Epoch, req.Spec); err != nil {
		return c.rollback(ctx, req, fmt.Errorf("restore on %s: %w", req.TargetNode, err))
	}

	// 5. Outcome. The instance now runs on the target; the commit moves
	// its node_id and closes the in-flight record.
	if err := c.raft.ProposeMigrateCommit(ctx, &goblinv1.MigrateCommit{
		InstanceUuid:    req.InstanceUUID,
		CheckpointEpoch: req.Epoch,
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED,
	}); err != nil {
		// The process IS on the target but the cluster still believes
		// it is on the source. Do not roll back: that would kill a
		// healthy process to satisfy bookkeeping. Surface it instead -
		// the record is stale, the instance is fine.
		c.log.LogAttrs(ctx, slog.LevelError, "migration completed but commit failed; instance record is stale",
			slog.String("instance_id", req.InstanceID),
			slog.String("target_node", req.TargetNode),
			slog.String("error", err.Error()))
		return fmt.Errorf("migration of %s completed on %s but the commit failed: %w",
			req.InstanceID, req.TargetNode, err)
	}

	c.log.LogAttrs(ctx, slog.LevelInfo, "migration completed",
		slog.String("instance_id", req.InstanceID),
		slog.String("source_node", req.SourceNode),
		slog.String("target_node", req.TargetNode),
		slog.Uint64("epoch", req.Epoch))
	return nil
}

// rollback restores the source from the image it dumped and records the
// migration as aborted.
//
// Restoring rather than "unfreezing" is the correct undo: criu dump
// stops the process, so the image is the only way back. That is also
// why the image is not deleted on failure.
func (c *Coordinator) rollback(ctx context.Context, req Request, cause error) error {
	err := c.nodes.Restore(ctx, req.SourceNode, req.InstanceID, req.InstanceUUID, req.Epoch, req.Spec)
	if err != nil {
		// Both the move and the undo failed. The instance is running
		// nowhere; say so distinctly so callers do not retry blindly.
		c.log.LogAttrs(ctx, slog.LevelError, "migration rollback failed; instance is not running",
			slog.String("instance_id", req.InstanceID),
			slog.String("source_node", req.SourceNode),
			slog.String("cause", cause.Error()),
			slog.String("rollback_error", err.Error()))
		c.abort(ctx, req, "rollback failed")
		return fmt.Errorf("%w: %s: %w (rollback: %w)", ErrStranded, req.InstanceID, cause, err)
	}

	c.abort(ctx, req, cause.Error())
	c.log.LogAttrs(ctx, slog.LevelWarn, "migration rolled back",
		slog.String("instance_id", req.InstanceID),
		slog.String("source_node", req.SourceNode),
		slog.String("cause", cause.Error()))
	return fmt.Errorf("%w: %s: %w", ErrRolledBack, req.InstanceID, cause)
}

// abort records the migration as ABORTED. A failure here is logged and
// swallowed: the caller is already returning the original error, and
// replacing it with a bookkeeping failure would hide the real cause.
// The in-flight record is left behind, which blocks retries until it is
// resolved - visible breakage in preference to a silent one.
func (c *Coordinator) abort(ctx context.Context, req Request, reason string) {
	if err := c.raft.ProposeMigrateCommit(ctx, &goblinv1.MigrateCommit{
		InstanceUuid:    req.InstanceUUID,
		CheckpointEpoch: req.Epoch,
		Outcome:         goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED,
	}); err != nil {
		c.log.LogAttrs(ctx, slog.LevelError, "recording migration abort failed; in-flight record remains",
			slog.String("instance_id", req.InstanceID),
			slog.String("reason", reason),
			slog.String("error", err.Error()))
	}
}
