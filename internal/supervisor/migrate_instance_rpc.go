package supervisor

import (
	"context"
	"fmt"
	"time"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/ident"
)

// Live instance migration (GOBLIN-DIV-031).
//
// Distinct from SchedulerRPC.MigrateJob, which REASSIGNS a job to
// another node - it stops work in one place and starts it in another,
// and the process does not survive. This moves a RUNNING process with
// its memory intact: the instance UUID is unchanged and only its
// locator moves. The two verbs share the RightJobMigrate right because
// both hand an instance to a different node; they are not otherwise
// interchangeable.

// MigrateInstanceRequest asks the leader to live-migrate one instance.
type MigrateInstanceRequest struct {
	// InstanceID is the canonical UUID string, as goblinctl prints it.
	InstanceID string
	// ToNode is the destination node id.
	ToNode string
}

// migrationEpoch derives the checkpoint epoch for a new attempt.
//
// Wall-clock milliseconds rather than a counter: the epoch only has to
// be monotonic per instance so a retry cannot collide with an earlier
// image, and a counter would need durable per-instance state that the
// FSM does not otherwise carry.
func migrationEpoch(now time.Time) uint64 {
	return uint64(now.UnixMilli())
}

// MigrateInstance live-migrates a running instance to another node.
func (s *SchedulerRPC) MigrateInstance(req *MigrateInstanceRequest, resp *string) error {
	if s.consensus == nil {
		return fmt.Errorf("consensus not initialized on this node")
	}
	if s.scheduler == nil {
		return fmt.Errorf("scheduler not initialized on this node")
	}

	uuid, err := ident.Parse(req.InstanceID)
	if err != nil {
		return fmt.Errorf("instance id %q: %w", req.InstanceID, err)
	}

	// Authorize against the INSTANCE, not the node or the job: a token
	// minted for one instance must not move another.
	payload, token, err := s.authorizeToken(capability.VerbJobMigrate, uuid)
	if err != nil {
		return err
	}

	inst, ok := s.consensus.GetInstance(req.InstanceID)
	if !ok {
		return fmt.Errorf("instance %s is not known to the cluster", req.InstanceID)
	}
	if inst.GetNodeId() == "" {
		return fmt.Errorf("instance %s has no current node", req.InstanceID)
	}
	if inst.GetNodeId() == req.ToNode {
		return fmt.Errorf("instance %s already runs on %s", req.InstanceID, req.ToNode)
	}

	// The destination must be able to instantiate the same agent type,
	// so the spec travels with the restore request.
	spec, err := s.scheduler.GetAgent(context.Background(), ident.String(inst.GetSpecUuid()))
	if err != nil {
		return fmt.Errorf("resolving spec for instance %s: %w", req.InstanceID, err)
	}

	coord := s.migrationCoordinator()
	if coord == nil {
		return fmt.Errorf("migration is not configured on this node")
	}

	err = coord.Migrate(context.Background(), migration.Request{
		InstanceID:   req.InstanceID,
		InstanceUUID: uuid,
		SourceNode:   inst.GetNodeId(),
		TargetNode:   req.ToNode,
		Epoch:        migrationEpoch(time.Now()),
		Rights:       payload.GetRights(),
		Token:        token,
		Spec:         spec,
	})
	if err != nil {
		// The coordinator's typed outcomes matter to an operator:
		// ErrRolledBack means the instance is still serving on its
		// source, ErrStranded means it is running nowhere. Both are
		// returned verbatim so goblinctl can say which.
		return err
	}

	*resp = fmt.Sprintf("instance %s migrated from %s to %s",
		req.InstanceID, inst.GetNodeId(), req.ToNode)
	return nil
}

// migrationCoordinator builds a coordinator from the collaborators the
// supervisor wired. Returns nil when migration is not configured, which
// MigrateInstance reports rather than half-running.
//
// Built per call rather than stored: a Coordinator is a plain struct
// over three interfaces with no state of its own, and constructing it
// here keeps the nil-check and the construction in one place.
func (s *SchedulerRPC) migrationCoordinator() *migration.Coordinator {
	if s.migrateNodes == nil || s.migrateRaft == nil {
		return nil
	}
	return migration.NewCoordinator(
		s.migrateRaft,
		s.migrateNodes,
		migration.NewRPCPuller(s.migrateNodes),
		s.migrateLogger,
	)
}
