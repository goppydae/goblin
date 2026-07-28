package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"syscall"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/gapi/core/procsig"
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
}

// StartAgentRequest defines payload for starting an agent instance
type StartAgentRequest struct {
	InstanceID string
	Spec       *goblinv1.AgentSpec
}

// StartAgentInstance instantiates the spec's agent type - which must be
// installed and discovery-verified on this node - and starts it under
// the instance id. Specs reference installed types; they never carry
// commands, so the discovery security model (R20) holds for scheduled
// work.
func (n *NodeRPC) StartAgentInstance(req *StartAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}
	if req.Spec == nil {
		return fmt.Errorf("start request for %s carries no spec", req.InstanceID)
	}

	a, err := n.agentMgr.Instantiate(req.InstanceID, req.Spec.Type)
	if err != nil {
		return fmt.Errorf("instantiate %q as %s: %w", req.Spec.Type, req.InstanceID, err)
	}

	runner, ok := a.(starter)
	if !ok {
		n.agentMgr.Deregister(req.InstanceID)
		return fmt.Errorf("agent type %q does not support direct execution", req.Spec.Type)
	}
	if err := runner.Start(context.Background()); err != nil {
		n.agentMgr.Deregister(req.InstanceID)
		return fmt.Errorf("start instance %s: %w", req.InstanceID, err)
	}

	n.tracker.Set(req.InstanceID, "running")
	// Capture the process identity: the pid feeds the gossip locator,
	// and the start epoch is the signal-delivery guard (DDR-5).
	if p, ok := a.(interface{ Pid() (int, bool) }); ok {
		if pid, running := p.Pid(); running {
			if pi, ierr := procsig.Identify(pid); ierr == nil {
				n.tracker.SetIdentity(req.InstanceID, pi.Pid, pi.StartEpoch)
			} else {
				n.tracker.SetIdentity(req.InstanceID, pid, 0)
			}
		}
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance started",
		logattr.InstanceID(req.InstanceID), logattr.Type(req.Spec.Type))
	*resp = fmt.Sprintf("instance %s started on node", req.InstanceID)
	return nil
}

// SignalAgentRequest delivers an authorized signal to an instance on
// this node.
type SignalAgentRequest struct {
	InstanceID string
	Signum     int32
}

// SignalAgentInstance delivers a signal through the start-epoch +
// pidfd guard. The request was already authorized at the leader's FSM;
// this node only guards delivery: a stale epoch means the process the
// caller meant is gone, so the delivery is refused with no retry.
func (n *NodeRPC) SignalAgentInstance(req *SignalAgentRequest, resp *string) error {
	info, ok := n.tracker.Get(req.InstanceID)
	if !ok || info.Pid <= 0 {
		return fmt.Errorf("instance %s has no running process on this node", req.InstanceID)
	}
	if err := procsig.Signal(info.Pid, info.StartEpoch, syscall.Signal(req.Signum)); err != nil {
		return fmt.Errorf("deliver signal %d to instance %s: %w", req.Signum, req.InstanceID, err)
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "signal delivered",
		logattr.InstanceID(req.InstanceID), logattr.Signum(int(req.Signum)))
	*resp = fmt.Sprintf("signal %d delivered to instance %s", req.Signum, req.InstanceID)
	return nil
}

// StopAgentRequest defines payload for stopping an agent instance
type StopAgentRequest struct {
	InstanceID string
}

// StopAgentInstance stops and deregisters an instance. Unknown instances
// succeed: a stop for something already gone is the desired state.
func (n *NodeRPC) StopAgentInstance(req *StopAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized")
	}

	// Remove from the tracker first so the exit's lifecycle event is not
	// misread as an unexpected failure.
	n.tracker.Remove(req.InstanceID)

	a := n.agentMgr.Get(req.InstanceID)
	if a == nil {
		*resp = fmt.Sprintf("instance %s not present (already stopped)", req.InstanceID)
		return nil
	}
	if runner, ok := a.(starter); ok {
		if err := runner.Stop(context.Background()); err != nil {
			return fmt.Errorf("stop instance %s: %w", req.InstanceID, err)
		}
	}
	n.agentMgr.Deregister(req.InstanceID)

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "instance stopped", logattr.InstanceID(req.InstanceID))
	*resp = fmt.Sprintf("instance %s stopped on node", req.InstanceID)
	return nil
}
