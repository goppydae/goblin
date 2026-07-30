package supervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"syscall"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/gapi/core/procsig"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// starter is the execution surface an instantiated agent must expose.
// GoAgent satisfies it; the Instantiate contract guarantees go agents.
type starter interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// NodeRPC executes the leader's placement decisions on this node: a
// scheduled instance becomes a real process under the embedded GAPI
// agent manager (the proto-2 Phase 3 node-dispatch seam, now closed).
type NodeRPC struct {
	agentMgr *gapiagentmgr.AgentManager
	tracker  *instanceTracker
	// images is this node's checkpoint image store. Nil when migration
	// is not configured, in which case the migration RPCs refuse rather
	// than inventing a directory.
	images *migration.Store
	// ckptTLS is the client TLS policy used when dialing a peer's
	// goblin-ckpt listener. Nil refuses the pull: this package must not
	// invent a verification policy for a transfer that carries an
	// instance's memory.
	ckptTLS *tls.Config
}

// StartAgentInstance instantiates the spec's agent type - which must be
// installed and discovery-verified on this node - and starts it under
// the instance id. Specs reference installed types; they never carry
// commands, so the discovery security model (R20) holds for scheduled
// work.
func (n *NodeRPC) StartAgentInstance(req *goblinv1.NodeStartAgentInstanceRequest, resp *goblinv1.NodeStartAgentInstanceResponse) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	spec := req.GetSpec()
	if spec == nil {
		return fmt.Errorf("%w: spec is required", ErrInvalidRequest)
	}
	instanceID := req.GetInstanceId()

	a, err := n.agentMgr.Instantiate(instanceID, spec.GetType())
	if err != nil {
		return fmt.Errorf("instantiate %q as %s: %w", spec.GetType(), instanceID, err)
	}

	runner, ok := a.(starter)
	if !ok {
		n.agentMgr.Deregister(instanceID)
		return fmt.Errorf("agent type %q does not support direct execution", spec.GetType())
	}
	if err := runner.Start(context.Background()); err != nil {
		n.agentMgr.Deregister(instanceID)
		return fmt.Errorf("start instance %s: %w", instanceID, err)
	}

	n.tracker.Set(instanceID, "running")
	// Capture the process identity: the pid feeds the gossip locator,
	// and the start epoch is the signal-delivery guard (DDR-5).
	if p, ok := a.(interface{ Pid() (int, bool) }); ok {
		if pid, running := p.Pid(); running {
			if pi, ierr := procsig.Identify(pid); ierr == nil {
				n.tracker.SetIdentity(instanceID, pi.Pid, pi.StartEpoch)
			} else {
				n.tracker.SetIdentity(instanceID, pid, 0)
			}
		}
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance started",
		logattr.InstanceID(instanceID), logattr.Type(spec.GetType()))
	resp.InstanceId = instanceID
	return nil
}

// SignalAgentInstance delivers a signal through the start-epoch +
// pidfd guard. The request was already authorized at the leader's FSM;
// this node only guards delivery: a stale epoch means the process the
// caller meant is gone, so the delivery is refused with no retry.
func (n *NodeRPC) SignalAgentInstance(req *goblinv1.NodeSignalAgentInstanceRequest, resp *goblinv1.NodeSignalAgentInstanceResponse) error {
	instanceID := req.GetInstanceId()
	info, ok := n.tracker.Get(instanceID)
	if !ok || info.Pid <= 0 {
		return fmt.Errorf("instance %s has no running process on this node", instanceID)
	}
	if err := procsig.Signal(info.Pid, info.StartEpoch, syscall.Signal(req.GetSignum())); err != nil {
		return fmt.Errorf("deliver signal %d to instance %s: %w", req.GetSignum(), instanceID, err)
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "signal delivered",
		logattr.InstanceID(instanceID), logattr.Signum(int(req.GetSignum())))
	resp.InstanceId = instanceID
	resp.Signum = req.GetSignum()
	return nil
}

// StopAgentInstance stops and deregisters an instance. Unknown instances
// succeed: a stop for something already gone is the desired state.
func (n *NodeRPC) StopAgentInstance(req *goblinv1.NodeStopAgentInstanceRequest, resp *goblinv1.NodeStopAgentInstanceResponse) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized")
	}
	instanceID := req.GetInstanceId()

	// Remove from the tracker first so the exit's lifecycle event is not
	// misread as an unexpected failure.
	n.tracker.Remove(instanceID)

	a := n.agentMgr.Get(instanceID)
	if a == nil {
		resp.InstanceId = instanceID
		resp.AlreadyStopped = true
		return nil
	}
	if runner, ok := a.(starter); ok {
		if err := runner.Stop(context.Background()); err != nil {
			return fmt.Errorf("stop instance %s: %w", instanceID, err)
		}
	}
	n.agentMgr.Deregister(instanceID)

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance stopped", logattr.InstanceID(instanceID))
	resp.InstanceId = instanceID
	return nil
}
