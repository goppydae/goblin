package consensus

import (
	"errors"
	"fmt"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Migration through the log (GOBLIN-DIV-018).
//
// The shape here is deliberate and follows DDR-3/DDR-4: identity lives
// in Raft and location lives in gossip, so moving an instance is a
// LOCATOR event, not a lifecycle event. The instance stays RUNNING for
// the whole migration and the lifecycle FSM never gains a state that
// returns to where it started - it stays monotonic, which is what makes
// the append-only tombstone model and the audit trail simple.
//
// Raft is still involved for two reasons that gossip cannot serve:
// a durable audit trail for the most consequential operation in the
// system (parity with SIGNAL under DDR-10), and a single point that can
// refuse a second concurrent migration of the same instance.

// ErrMigrationInFlight is returned when a second MIGRATE_BEGIN arrives
// for an instance that is already migrating. Typed because a proposer
// retrying after a lost response must distinguish "someone else is
// moving this" from "your request was malformed".
var ErrMigrationInFlight = errors.New("migration already in flight for this instance")

// ErrNoMigrationInFlight is returned when a commit arrives with no
// matching begin.
var ErrNoMigrationInFlight = errors.New("no migration in flight for this instance")

// applyMigrateBegin records the intent to move one instance. Callers
// hold f.mu.
func (f *FSM) applyMigrateBegin(mb *goblinv1.MigrateBegin) interface{} {
	if mb == nil {
		return fmt.Errorf("MIGRATE_BEGIN command with no payload")
	}
	instID := ident.String(mb.InstanceUuid)
	if instID == "" {
		return fmt.Errorf("MIGRATE_BEGIN with malformed UUID (%d bytes)", len(mb.InstanceUuid))
	}
	if mb.TargetNodeId == "" {
		return fmt.Errorf("MIGRATE_BEGIN for instance %s names no target node", instID)
	}

	inst, ok := f.instances[instID]
	if !ok {
		return fmt.Errorf("MIGRATE_BEGIN for unknown instance %s", instID)
	}

	// Only a running instance can be checkpointed. DRAINING and
	// STOPPING are excluded deliberately: those are already on their
	// way down, and migrating one would race its own shutdown.
	if inst.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		return fmt.Errorf("MIGRATE_BEGIN rejected: instance %s is %s, not RUNNING", instID, inst.State)
	}
	if inst.NodeId == mb.TargetNodeId {
		return fmt.Errorf("MIGRATE_BEGIN rejected: instance %s is already on node %s", instID, mb.TargetNodeId)
	}

	required, err := capability.RightForVerb(capability.VerbJobMigrate)
	if err != nil {
		return fmt.Errorf("MIGRATE_BEGIN rejected for instance %s: %w", instID, err)
	}
	if mb.Rights&required != required {
		return fmt.Errorf("MIGRATE_BEGIN rejected for instance %s: rights %#x lack %#x",
			instID, mb.Rights, required)
	}

	if existing, busy := f.migrations[instID]; busy {
		return fmt.Errorf("%w: instance %s is moving to %s at epoch %d",
			ErrMigrationInFlight, instID, existing.TargetNodeId, existing.CheckpointEpoch)
	}

	f.migrations[instID] = &goblinv1.MigrationRecord{
		InstanceUuid:    append([]byte(nil), mb.InstanceUuid...),
		SourceNodeId:    inst.NodeId,
		TargetNodeId:    mb.TargetNodeId,
		CheckpointEpoch: mb.CheckpointEpoch,
	}
	return nil
}

// applyMigrateCommit closes a migration, moving the instance's node_id
// on success and leaving it alone on abort. Callers hold f.mu.
func (f *FSM) applyMigrateCommit(mc *goblinv1.MigrateCommit) interface{} {
	if mc == nil {
		return fmt.Errorf("MIGRATE_COMMIT command with no payload")
	}
	instID := ident.String(mc.InstanceUuid)
	if instID == "" {
		return fmt.Errorf("MIGRATE_COMMIT with malformed UUID (%d bytes)", len(mc.InstanceUuid))
	}

	rec, ok := f.migrations[instID]
	if !ok {
		return fmt.Errorf("%w: instance %s", ErrNoMigrationInFlight, instID)
	}
	// A commit for a different epoch is a late message from a
	// superseded attempt. Applying it would close the CURRENT migration
	// on the strength of an older one's outcome.
	if rec.CheckpointEpoch != mc.CheckpointEpoch {
		return fmt.Errorf("MIGRATE_COMMIT rejected: instance %s is migrating at epoch %d, commit carries %d",
			instID, rec.CheckpointEpoch, mc.CheckpointEpoch)
	}

	inst, ok := f.instances[instID]
	if !ok {
		// The instance vanished mid-migration. Drop the record so the
		// UUID is not left permanently unmigratable, and say so.
		delete(f.migrations, instID)
		return fmt.Errorf("MIGRATE_COMMIT for instance %s which no longer exists", instID)
	}

	switch mc.Outcome {
	case goblinv1.MigrateOutcome_MIGRATE_OUTCOME_COMPLETED:
		inst.NodeId = rec.TargetNodeId
		delete(f.migrations, instID)
		return nil

	case goblinv1.MigrateOutcome_MIGRATE_OUTCOME_ABORTED:
		// Nothing to undo in Raft: the instance never stopped being
		// RUNNING on its source, and the source restores from the same
		// image it dumped.
		delete(f.migrations, instID)
		return nil

	default:
		// UNSPECIFIED lands here. Leave the record in place: refusing to
		// guess keeps the migration visibly open rather than silently
		// resolving it in one direction.
		return fmt.Errorf("MIGRATE_COMMIT for instance %s has unspecified outcome; refusing to guess", instID)
	}
}

// MigrationsInFlight lists every migration currently recorded as in
// flight.
//
// It exists for the orphan sweep (GOBLIN-DIV-049): nothing else ever
// clears one of these records. Only a MIGRATE_COMMIT does, and only the
// leader can propose one - so a leader that dies mid-migration leaves a
// record no surviving node will ever retire, and the reconciler now
// honours those records. Without a sweep that would trade a duplicated
// instance for a permanently unrecoverable one.
func (f *FSM) MigrationsInFlight() []*goblinv1.MigrationRecord {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*goblinv1.MigrationRecord, 0, len(f.migrations))
	for _, rec := range f.migrations {
		// Copied for the same reason MigrationInFlight copies: a read
		// must not hand out a pointer into replicated state.
		out = append(out, &goblinv1.MigrationRecord{
			InstanceUuid:    append([]byte(nil), rec.InstanceUuid...),
			SourceNodeId:    rec.SourceNodeId,
			TargetNodeId:    rec.TargetNodeId,
			CheckpointEpoch: rec.CheckpointEpoch,
		})
	}
	return out
}

// MigrationInFlight reports the in-flight migration for an instance.
// Read path for goblinctl and the reconciler; takes the read lock.
func (f *FSM) MigrationInFlight(instanceUUID []byte) (*goblinv1.MigrationRecord, bool) {
	instID := ident.String(instanceUUID)
	if instID == "" {
		return nil, false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	rec, ok := f.migrations[instID]
	if !ok {
		return nil, false
	}
	// Copied: the caller must not be able to mutate FSM state through a
	// read, which would diverge replicas.
	return &goblinv1.MigrationRecord{
		InstanceUuid:    append([]byte(nil), rec.InstanceUuid...),
		SourceNodeId:    rec.SourceNodeId,
		TargetNodeId:    rec.TargetNodeId,
		CheckpointEpoch: rec.CheckpointEpoch,
	}, true
}
