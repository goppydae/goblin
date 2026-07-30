package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/goppydae/goblin/internal/ident"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// ReconcileAgents ensures the actual state matches the desired state for all agents.
func (s *Scheduler) ReconcileAgents(ctx context.Context) error {
	s.noteReconcileBaseline()
	specs, err := s.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("failed to list agent specs: %w", err)
	}

	for _, spec := range specs {
		if err := s.reconcileAgent(ctx, spec); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to reconcile agent", logattr.SpecID(ident.String(spec.SpecUuid)), logattr.Err(err))
		}
	}
	return nil
}

func (s *Scheduler) reconcileAgent(ctx context.Context, spec *goblinv1.AgentSpec) error {
	instances, err := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	// Filter healthy/running instances
	var active []*goblinv1.AgentInstance
	for _, inst := range instances {
		instID := ident.String(inst.InstanceUuid)
		failed := ""
		// Heartbeat verdict overrides recorded state: a node that
		// reported failure, or has gone silent past the staleness
		// window, makes its running instances dead (phase 2b).
		if inst.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING && s.instanceUnhealthy(instID) {
			failed = "heartbeat lost or node reported failure"
		}
		if inst.State == goblinv1.InstanceState_INSTANCE_STATE_ADMITTED && s.pendingStale(instID) {
			failed = "stuck pending past the staleness window"
		}
		switch {
		case failed != "":
			// Terminate through the FSM; the UUID is tombstoned and the
			// replica count drops, so a replacement is admitted below.
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "instance failed, triggering recovery", logattr.InstanceID(instID), logattr.Reason(failed))
			if err := s.store.TransitionInstance(ctx, inst.InstanceUuid, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, failed); err != nil {
				return fmt.Errorf("terminate failed instance %s: %w", instID, err)
			}
			s.forgetHeartbeat(instID)
		case inst.State == goblinv1.InstanceState_INSTANCE_STATE_TERMINATED:
			// Terminal and already tombstoned: archive compacts the
			// record (the tombstone stays forever).
			if err := s.store.TransitionInstance(ctx, inst.InstanceUuid, goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED, "archived by reconciler"); err != nil {
				return fmt.Errorf("archive instance %s: %w", instID, err)
			}
			s.forgetHeartbeat(instID)
		case inst.State >= goblinv1.InstanceState_INSTANCE_STATE_ADMITTED &&
			inst.State <= goblinv1.InstanceState_INSTANCE_STATE_RUNNING:
			active = append(active, inst)
		}
	}

	currentCount := len(active)
	desiredCount := int(spec.Replicas)

	if currentCount < desiredCount {
		// Scale Up
		needed := desiredCount - currentCount
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "scaling up agent", logattr.SpecID(ident.String(spec.SpecUuid)), logattr.Count(needed))
		for i := 0; i < needed; i++ {
			if err := s.createInstance(ctx, spec); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to create instance", logattr.SpecID(ident.String(spec.SpecUuid)), logattr.Err(err))
			}
		}
	} else if currentCount > desiredCount {
		// Scale Down
		excess := currentCount - desiredCount
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "scaling down agent", logattr.SpecID(ident.String(spec.SpecUuid)), logattr.Count(excess))
		// Simple strategy: Remove newest (or random)
		// 'active' might not be sorted. Just pick last 'excess' elements.
		for i := 0; i < excess; i++ {
			inst := active[len(active)-1-i]
			if err := s.terminateInstance(ctx, inst); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to terminate instance", logattr.InstanceID(ident.String(inst.InstanceUuid)), logattr.Err(err))
			}
		}
	}

	return nil
}

func (s *Scheduler) createInstance(ctx context.Context, spec *goblinv1.AgentSpec) error {
	// 1. Get Candidates
	candidates, err := s.getCandidates(ctx)
	if err != nil {
		return err
	}

	// 2. Select Node via Placement Engine
	nodeID, err := s.placement.SelectNode(spec, candidates)
	if err != nil {
		return fmt.Errorf("placement failed: %w", err)
	}

	// 3. Admit through Raft. The UUIDv7 is minted here - at the leader,
	// at propose time, before any process exists (admission-time
	// identity, GOBLIN-DIV-014); the FSM records it and enforces
	// never-reuse against the tombstone set.
	instance := &goblinv1.AgentInstance{
		InstanceUuid: ident.NewV7(),
		SpecUuid:     spec.SpecUuid,
		NodeId:       nodeID,
		State:        goblinv1.InstanceState_INSTANCE_STATE_ADMITTED,
	}
	if err := s.store.Admit(ctx, spec.SpecUuid, instance.InstanceUuid, nodeID); err != nil {
		return fmt.Errorf("failed to admit instance: %w", err)
	}

	// 4. Trigger Start on Node (Async to avoid blocking Reconciler loop).
	// The reconciler-loop ctx (not Background) so in-flight starts abort
	// at shutdown instead of outliving the supervisor, and counted on
	// s.dispatches so RunReconciler can join them rather than trusting
	// cancellation to have been noticed (AwaitDispatches).
	s.dispatches.Add(1)
	go func() {
		defer s.dispatches.Done()
		if err := s.startAgentOnNode(ctx, nodeID, instance, spec); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to start agent on node", logattr.InstanceID(ident.String(instance.InstanceUuid)), logattr.NodeID(nodeID), logattr.Err(err))
			// A dispatch failure must not leave a pending ghost: mark
			// the instance failed so the next reconcile re-places it.
			reason := "dispatch failed: " + err.Error()
			if serr := s.store.TransitionInstance(ctx, instance.InstanceUuid, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, reason); serr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to mark instance failed", logattr.InstanceID(ident.String(instance.InstanceUuid)), logattr.Err(serr))
			}
		}
	}()

	return nil
}

func (s *Scheduler) terminateInstance(ctx context.Context, inst *goblinv1.AgentInstance) error {
	instID := ident.String(inst.InstanceUuid)
	// 1. Trigger Stop on Node
	if err := s.stopAgentOnNode(ctx, inst.NodeId, instID); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to stop agent on node, continuing cleanup", logattr.Err(err))
	}

	// 2. Terminate through the FSM (tombstoned; archived on a later pass)
	return s.store.TransitionInstance(ctx, inst.InstanceUuid, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, "scaled down")
}

// Internal helpers

func (s *Scheduler) getCandidates(ctx context.Context) ([]CandidateNode, error) {
	members := s.cluster.Members() // From Serf
	if len(members) == 0 {
		return nil, fmt.Errorf("no cluster members found")
	}

	var candidates []CandidateNode

	// Pre-calculate usage for all nodes (Optimization: batch this)
	usageMap, err := s.calculateUsage(ctx, members)
	if err != nil {
		// Fallback to empty usage if fail? Or error?
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to calculate usage", logattr.Err(err))
		usageMap = make(map[string]nodeUsage)
	}

	// Count scheduled instances per node so spread placement sees them:
	// job usage alone would pile every instance onto one node. Instances
	// saved earlier in the same reconcile pass are visible here, so
	// multi-replica placement spreads within a single pass too.
	instCounts := make(map[string]int)
	if instances, ierr := s.ListInstances(ctx, ""); ierr == nil {
		for _, inst := range instances {
			if inst.State == goblinv1.InstanceState_INSTANCE_STATE_RUNNING ||
				inst.State == goblinv1.InstanceState_INSTANCE_STATE_ADMITTED {
				instCounts[inst.NodeId]++
			}
		}
	}

	for _, m := range members {
		if m.Status != 1 { // Alive
			continue
		}

		u := usageMap[m.Name]

		// Fill CandidateNode
		// Tags must have cpu/memory
		// For now, assume usageMap has current data.
		// We need TOTAL resources from tags.

		candidates = append(candidates, CandidateNode{
			ID:   m.Name,
			Tags: m.Tags,
			Resources: NodeResources{
				TotalCPU:   u.cpuTotal,
				UsedCPU:    u.cpuUsed,
				TotalMem:   u.memTotal,
				UsedMem:    u.memUsed,
				AgentCount: u.jobCount + instCounts[m.Name],
			},
		})
	}
	return candidates, nil
}

// Stubs for RPC calls (Phase 3.4 will replace these) -> Real Implementation Phase 3.5

func (s *Scheduler) startAgentOnNode(ctx context.Context, nodeID string, inst *goblinv1.AgentInstance, spec *goblinv1.AgentSpec) (err error) {
	addr, err := s.getNodeAddress(ctx, nodeID)
	if err != nil {
		return err
	}

	// 2. Create Client
	if s.clientFactory == nil {
		return fmt.Errorf("rpc client factory not initialized")
	}
	// addr already includes port from getNodeAddress
	client, err := s.clientFactory(addr)
	if err != nil {
		return fmt.Errorf("failed to create rpc client: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close rpc client: %w", cerr)
		}
	}()

	// 3. Call NodeRPC
	// We need to define StartAgentRequest within scheduler or import it from supervisor?
	// Problem: StartAgentRequest is in supervisor/node_rpc.go -> import cycle if we import supervisor.
	// We should define the request struct in a shared place or use map/standard types.
	// But CLI/Server use the struct.
	// Solution: Use Anonymous struct or map since our generic client supports it,
	// OR define local struct matching the wire format.

	// Better: Move connection structs to `goblin/proto` or a shared `pkg/api`?
	// Phase 3.4/5 goal isn't huge refactor.
	// Let's use anonymous struct matching target.

	payload := struct {
		InstanceID string
		Spec       *goblinv1.AgentSpec
	}{
		InstanceID: ident.String(inst.InstanceUuid),
		Spec:       spec,
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "calling start agent instance", logattr.NodeID(nodeID), logattr.Addr(addr))
	var resp string
	if err := client.CallJSON("NodeRPC.StartAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "start agent rpc succeeded", logattr.Response(resp))

	// Record the transition to RUNNING through the FSM
	inst.State = goblinv1.InstanceState_INSTANCE_STATE_RUNNING
	return s.store.TransitionInstance(ctx, inst.InstanceUuid, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, "")
}

func (s *Scheduler) stopAgentOnNode(ctx context.Context, nodeID, instanceID string) (err error) {
	addr, err := s.getNodeAddress(ctx, nodeID)
	if err != nil {
		return err
	}

	if s.clientFactory == nil {
		return fmt.Errorf("rpc client factory not initialized")
	}
	client, err := s.clientFactory(addr)
	if err != nil {
		return fmt.Errorf("failed to create rpc client: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close rpc client: %w", cerr)
		}
	}()

	payload := struct {
		InstanceID string
	}{
		InstanceID: instanceID,
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "calling stop agent instance", logattr.NodeID(nodeID), logattr.Addr(addr))
	var resp string
	if err := client.CallJSON("NodeRPC.StopAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "stop agent rpc succeeded", logattr.Response(resp))
	return nil
}

// SignalOnNode dispatches an authorized signal delivery to the target
// node's NodeRPC; the node's epoch guard makes the final call.
func (s *Scheduler) SignalOnNode(ctx context.Context, nodeID, instanceID string, signum int32) (err error) {
	addr, err := s.getNodeAddress(ctx, nodeID)
	if err != nil {
		return err
	}
	if s.clientFactory == nil {
		return fmt.Errorf("rpc client factory not initialized")
	}
	client, err := s.clientFactory(addr)
	if err != nil {
		return fmt.Errorf("failed to create rpc client: %w", err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close rpc client: %w", cerr)
		}
	}()

	payload := struct {
		InstanceID string
		Signum     int32
	}{InstanceID: instanceID, Signum: signum}
	var resp string
	if err := client.CallJSON("NodeRPC.SignalAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "signal rpc succeeded", logattr.Response(resp))
	return nil
}

func (s *Scheduler) getNodeAddress(ctx context.Context, nodeID string) (string, error) {
	members := s.cluster.Members()
	for _, m := range members {
		if m.Name == nodeID {
			// Single-listener model: the member's advertised address IS
			// its RPC endpoint (every plane shares it, routed by ALPN).
			return net.JoinHostPort(m.Addr.String(), strconv.Itoa(int(m.Port))), nil
		}
	}
	return "", fmt.Errorf("node %s not found", nodeID)
}

// dispatchDrainTimeout bounds how long RunReconciler waits for in-flight
// agent dispatches once its context is cancelled. A dispatch whose ctx is
// already cancelled fails fast, so the normal drain is immediate; the
// bound exists for a start parked in an RPC that ignores cancellation, so
// shutdown cannot hang on one unresponsive node.
const dispatchDrainTimeout = 10 * time.Second

// drainDispatches joins the dispatch goroutines this loop started.
//
// ctx is already cancelled here, so the drain carries its own deadline
// detached from it - waiting on a cancelled context would return at once
// and defeat the join.
func (s *Scheduler) drainDispatches(ctx context.Context) {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchDrainTimeout)
	defer cancel()
	if err := s.AwaitDispatches(drainCtx); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent dispatches outlived the reconciler drain deadline", logattr.Err(err))
		return
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "reconciler stopped, dispatches drained")
}

// RunReconciler starts the periodic reconciliation loop
func (s *Scheduler) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "reconciler started")
	for {
		select {
		case <-ctx.Done():
			s.drainDispatches(ctx)
			return
		case <-ticker.C:
		case <-s.reconcileKick:
		}
		// Only the leader writes; followers skip the pass (R7). Losing
		// leadership clears the heartbeat grace baseline so regaining
		// it starts fresh.
		if !s.leading() {
			s.clearReconcileBaseline()
			continue
		}
		if err := s.ReconcileAgents(ctx); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "reconcile cycle failed", logattr.Err(err))
		}
	}
}

// NodeAddress resolves a node id to its control-plane address.
//
// Exported for the migration coordinator, which must reach two specific
// nodes by id rather than following a placement decision. Thin wrapper
// over the same membership lookup the reconciler uses, so the two
// cannot disagree about where a node is.
func (s *Scheduler) NodeAddress(ctx context.Context, nodeID string) (string, error) {
	return s.getNodeAddress(ctx, nodeID)
}
