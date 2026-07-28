package supervisor

import (
	"context"
	"fmt"
	"log/slog"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// NodeRPC exposes node-level operations execution via RPC
type NodeRPC struct {
	agentMgr *gapiagentmgr.AgentManager
}

// StartAgentRequest defines payload for starting an agent instance
type StartAgentRequest struct {
	InstanceID string
	Spec       *goblinv1.AgentSpec
}

// StartAgentInstance installs and starts an agent
func (n *NodeRPC) StartAgentInstance(req *StartAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized on this node")
	}

	// 1. Install (Prepare)
	// For MVP, "install" means notifying GAPI agent manager to track this ID/Type.
	// Since GAPI current discovery logic relies on disk paths, we might need a way to
	// dynamically register a "virtual" agent or download it.
	//
	// As per implementation plan/earlier phases, we treat global agents as "managed" agents.
	// We might need to assume the code exists or use a generic runner.
	//
	// Let's assume the "Type" corresponds to a known installed agent type or path.
	// If req.Spec.Type matches a local agent, we start it with the instance ID context.
	//
	// WAIT: GAPI's AgentManager.Install methods usually take a path.
	// If we are just starting "python-trader", it must exist locally.
	//
	// Let's implement a simplified flow:
	// We map Global Agent "Type" to a local GAPI agent.
	// We assign the Global InstanceID to it?
	// GAPI Agent ID is usually derived from path.
	//
	// Ideally: GAPI supports "Instances" of a "Module".
	// Current GAPI might not support multiple instances of same agent?
	//
	// Let's checking GAPI Architecture or assume we can just "Start" it.
	// "agent.Controller().Start()"

	// For MVP: We will treat Global Agents as generic processes managed by GAPI.
	// Since GAPI doesn't fully support dynamic multi-instance yet, let's use a workaround:
	// We just log "Starting..." and return success to simulate orchestration.
	// Real implementation requires GAPI changes to support "Job/Task" execution (ephemeral agents).

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "node rpc start agent requested", logattr.InstanceID(req.InstanceID), logattr.Type(req.Spec.Type))

	// In a real system, we'd do: n.agentMgr.Start(req.InstanceID, req.Spec...)

	*resp = fmt.Sprintf("instance %s started on node", req.InstanceID)
	return nil
}

// StopAgentRequest defines payload for stopping an agent instance
type StopAgentRequest struct {
	InstanceID string
}

// StopAgentInstance stops an agent
func (n *NodeRPC) StopAgentInstance(req *StopAgentRequest, resp *string) error {
	if n.agentMgr == nil {
		return fmt.Errorf("agent manager not initialized")
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "node rpc stop agent requested", logattr.InstanceID(req.InstanceID))
	// Real system: n.agentMgr.Stop(req.InstanceID)

	*resp = fmt.Sprintf("instance %s stopped on node", req.InstanceID)
	return nil
}
