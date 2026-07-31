package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/core/procsig"

	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// Node-local halves of a migration (GOBLIN-DIV-018).
//
// The source dumps into its image store; the destination pulls the
// image (core/migration) and restores from it. Neither side decides
// that a migration should happen - that is the leader's MIGRATE_BEGIN -
// and neither moves the instance's Raft record. All they change is
// where the process runs, which the heartbeat publishes as a locator.

// CheckpointAgentInstance dumps a running instance into this node's
// image store, leaving it stopped.
//
// Stopped is the point. The image is the rollback artifact for the
// migration, and a source that kept running past the state its image
// captured would have diverged from it before the destination even
// started restoring.
func (n *NodeRPC) CheckpointAgentInstance(req *goblinv1.NodeCheckpointAgentInstanceRequest, resp *goblinv1.NodeCheckpointAgentInstanceResponse) error {
	if err := n.requireOperatorRegistry("node.checkpoint"); err != nil {
		return err
	}
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}
	instanceID := req.GetInstanceId()

	a := n.agentMgr.Get(instanceID)
	if a == nil {
		return fmt.Errorf("instance %s is not running on this node", instanceID)
	}
	ckpt, ok := a.(lifecycle.Checkpointer)
	if !ok {
		// Declining the capability is a legitimate answer, not a bug:
		// an in-process runner has nothing for CRIU to dump. The
		// orchestrator should have asserted this at admission, so
		// reaching here means the assertion was skipped.
		return fmt.Errorf("instance %s runs an agent type that cannot be checkpointed", instanceID)
	}

	// Capture the pid before the dump: afterwards the runner has no
	// process to report, and we need it to know when the PID is free.
	var dumpedPid int
	if p, ok := a.(interface{ Pid() (int, bool) }); ok {
		if pid, running := p.Pid(); running {
			dumpedPid = pid
		}
	}

	dir, err := n.images.Create(req.GetInstanceUuid(), req.GetEpoch())
	if err != nil {
		return fmt.Errorf("checkpoint instance %s: %w", instanceID, err)
	}
	if err := ckpt.Checkpoint(context.Background(), dir); err != nil {
		return fmt.Errorf("checkpoint instance %s: %w", instanceID, err)
	}

	// Do not return until the PID is actually free (GOBLIN-DIV-031).
	//
	// criu dump kills the process, but a killed child of a live parent
	// is a ZOMBIE and a zombie still holds its PID. A restore that
	// reclaims that PID via clone3(set_tid) then fails with "Can't fork
	// for <pid>: File exists". Waiting here rather than in the
	// coordinator keeps the knowledge where the pid is: the node.
	//
	// This matters most on the ROLLBACK path, where the source restores
	// into the very namespace it just vacated. A cross-node restore
	// lands in a fresh PID namespace and would not collide - but
	// returning early would still let the coordinator race a reap that
	// has not happened.
	if dumpedPid > 0 {
		if err := waitForPidRelease(dumpedPid, pidReleaseTimeout); err != nil {
			return fmt.Errorf("checkpoint instance %s: %w", instanceID, err)
		}
	}

	// The process is stopped; its locator is no longer valid here. Zero
	// the identity so this node stops publishing a pid that a signal
	// could still be routed to.
	n.tracker.SetIdentity(instanceID, 0, 0)
	n.tracker.Set(instanceID, "checkpointed")

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance checkpointed",
		logattr.InstanceID(instanceID), slog.String("dir", dir), slog.Uint64("epoch", req.GetEpoch()))
	resp.ImageDir = dir
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
func (n *NodeRPC) RestoreAgentInstance(req *goblinv1.NodeRestoreAgentInstanceRequest, resp *goblinv1.NodeRestoreAgentInstanceResponse) error {
	if err := n.requireOperatorRegistry("node.restore"); err != nil {
		return err
	}
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}
	instanceID := req.GetInstanceId()
	spec := req.GetSpec()
	// This method's own structural validation stays a plain error
	// (unlike StartAgentInstance's ErrInvalidRequest guard): the typed
	// classification for a decoded-but-empty spec is applied at the
	// quic_handlers.go dispatch layer, which is the boundary that can
	// see ErrInvalidRequest without core/migration's callers of this
	// same message needing to import internal/supervisor.
	if spec == nil {
		return fmt.Errorf("restore request for %s carries no spec", instanceID)
	}

	dir, err := n.images.Dir(req.GetInstanceUuid(), req.GetEpoch())
	if err != nil {
		return fmt.Errorf("restore instance %s: %w", instanceID, err)
	}

	// Reuse an existing registration rather than instantiating over it.
	//
	// The ROLLBACK path lands here: the source still has the instance
	// registered from before its dump, so an unconditional Instantiate
	// fails with "already registered" and the rollback can never
	// succeed - which turns every recoverable migration failure into
	// ErrStranded. The two-node test proved exactly that.
	//
	// A fresh registration is only correct on the destination, which has
	// never seen this instance.
	a := n.agentMgr.Get(instanceID)
	fresh := a == nil
	if fresh {
		var err error
		a, err = n.agentMgr.Instantiate(instanceID, spec.GetType())
		if err != nil {
			return fmt.Errorf("instantiate %q as %s: %w", spec.GetType(), instanceID, err)
		}
	}

	ckpt, ok := a.(lifecycle.Checkpointer)
	if !ok {
		if fresh {
			n.agentMgr.Deregister(instanceID)
		}
		return fmt.Errorf("agent type %q cannot be restored from a checkpoint", spec.GetType())
	}
	if err := ckpt.Restore(context.Background(), dir); err != nil {
		// Only tear down a registration this call created. Deregistering
		// one that predates us would strip the source of an instance it
		// is about to keep running.
		if fresh {
			n.agentMgr.Deregister(instanceID)
		}
		return fmt.Errorf("restore instance %s: %w", instanceID, err)
	}

	n.tracker.Set(instanceID, "running")
	n.captureIdentity(instanceID, a)

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance restored",
		logattr.InstanceID(instanceID), slog.String("dir", dir), slog.Uint64("epoch", req.GetEpoch()))
	resp.InstanceId = instanceID
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
func (n *NodeRPC) PullCheckpoint(req *goblinv1.NodePullCheckpointRequest, resp *goblinv1.NodePullCheckpointResponse) error {
	if n.images == nil {
		return fmt.Errorf("checkpoint store not configured on this node")
	}
	if n.ckptTLS == nil {
		return fmt.Errorf("checkpoint transport TLS not configured on this node")
	}
	instanceID := req.GetInstanceId()
	sourceAddr := req.GetSourceAddr()
	if sourceAddr == "" {
		return fmt.Errorf("pull request for %s names no source address", instanceID)
	}

	dir, err := migration.DialAndFetch(context.Background(), sourceAddr, n.ckptTLS,
		n.images, req.GetInstanceUuid(), req.GetEpoch(), req.GetToken())
	if err != nil {
		return fmt.Errorf("pulling image for %s from %s: %w", instanceID, sourceAddr, err)
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "checkpoint image pulled",
		logattr.InstanceID(instanceID), slog.String("source", sourceAddr),
		slog.String("dir", dir), slog.Uint64("epoch", req.GetEpoch()))
	resp.ImageDir = dir
	return nil
}

// pidReleaseTimeout bounds the wait for a dumped process to be reaped.
// Generous, because the reaper is the subreaper loop rather than this
// goroutine, but bounded: a migration that hangs here holds a stopped
// process, which is worse than a migration that fails and rolls back.
const pidReleaseTimeout = 30 * time.Second

// waitForPidRelease blocks until pid leaves the process table.
//
// Presence is the test, not liveness: a zombie is not alive but still
// occupies its PID, which is exactly the condition that breaks restore.
// Off Linux /proc does not exist, so this returns immediately - correct
// enough, since checkpoint/restore is Linux-only by design.
func waitForPidRelease(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pid %d still held %s after the dump; a restore could not reclaim it",
				pid, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Images is this node's checkpoint image store.
func (n *NodeRPC) Images() *migration.Store { return n.images }
