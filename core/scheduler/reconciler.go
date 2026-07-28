package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
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
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to reconcile agent", logattr.SpecID(spec.Id), logattr.Err(err))
		}
	}
	return nil
}

func (s *Scheduler) reconcileAgent(ctx context.Context, spec *goblinv1.AgentSpec) error {
	instances, err := s.ListInstances(ctx, spec.Id)
	if err != nil {
		return fmt.Errorf("failed to list instances: %w", err)
	}

	// Filter healthy/running instances
	var active []*goblinv1.AgentInstance
	for _, inst := range instances {
		// Heartbeat verdict overrides recorded state: a node that
		// reported failure, or has gone silent past the staleness
		// window, makes its running instances dead (phase 2b).
		if inst.State == "running" && s.instanceUnhealthy(inst.InstanceId) {
			inst.State = "failed"
		}
		if inst.State == "pending" && s.pendingStale(inst.InstanceId) {
			inst.State = "failed"
		}
		switch inst.State {
		case "running", "pending":
			active = append(active, inst)
		case "failed":
			// Handle failure: Restart/Reschedule
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "instance failed, triggering recovery", logattr.InstanceID(inst.InstanceId))
			// For now, just remove it from active list so it gets replaced
			// In real impl, we might want to cleanup the old node first
			if err := s.DeleteInstance(ctx, inst.InstanceId); err != nil {
				return fmt.Errorf("delete failed instance %s: %w", inst.InstanceId, err)
			}
			s.forgetHeartbeat(inst.InstanceId)
		}
	}

	currentCount := len(active)
	desiredCount := int(spec.Replicas)

	if currentCount < desiredCount {
		// Scale Up
		needed := desiredCount - currentCount
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "scaling up agent", logattr.SpecID(spec.Id), logattr.Count(needed))
		for i := 0; i < needed; i++ {
			if err := s.createInstance(ctx, spec); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to create instance", logattr.SpecID(spec.Id), logattr.Err(err))
			}
		}
	} else if currentCount > desiredCount {
		// Scale Down
		excess := currentCount - desiredCount
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "scaling down agent", logattr.SpecID(spec.Id), logattr.Count(excess))
		// Simple strategy: Remove newest (or random)
		// 'active' might not be sorted. Just pick last 'excess' elements.
		for i := 0; i < excess; i++ {
			inst := active[len(active)-1-i]
			if err := s.terminateInstance(ctx, inst); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to terminate instance", logattr.InstanceID(inst.InstanceId), logattr.Err(err))
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

	// 3. Create Instance Record
	instance := &goblinv1.AgentInstance{
		InstanceId: fmt.Sprintf("%s-%s", spec.Id, uuid.New().String()[:8]),
		SpecId:     spec.Id,
		NodeId:     nodeID,
		State:      "pending",
		Health:     &goblinv1.HealthStatus{Status: "healthy"},
	}

	if err := s.SaveInstance(ctx, instance); err != nil {
		return fmt.Errorf("failed to save instance: %w", err)
	}

	// 4. Trigger Start on Node (Async to avoid blocking Reconciler loop).
	// The reconciler-loop ctx (not Background) so in-flight starts abort
	// at shutdown instead of outliving the supervisor.
	go func() {
		if err := s.startAgentOnNode(ctx, nodeID, instance, spec); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to start agent on node", logattr.InstanceID(instance.InstanceId), logattr.NodeID(nodeID), logattr.Err(err))
			// A dispatch failure must not leave a pending ghost: mark
			// the instance failed so the next reconcile re-places it.
			instance.State = "failed"
			if serr := s.SaveInstance(ctx, instance); serr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelError, "failed to mark instance failed", logattr.InstanceID(instance.InstanceId), logattr.Err(serr))
			}
		}
	}()

	return nil
}

func (s *Scheduler) terminateInstance(ctx context.Context, inst *goblinv1.AgentInstance) error {
	// 1. Trigger Stop on Node
	if err := s.stopAgentOnNode(ctx, inst.NodeId, inst.InstanceId); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to stop agent on node, continuing cleanup", logattr.Err(err))
	}

	// 2. Delete Record
	return s.DeleteInstance(ctx, inst.InstanceId)
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
			if inst.State == "running" || inst.State == "pending" {
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
		InstanceID: inst.InstanceId,
		Spec:       spec,
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "calling start agent instance", logattr.NodeID(nodeID), logattr.Addr(addr))
	var resp string
	if err := client.Call("NodeRPC.StartAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "start agent rpc succeeded", logattr.Response(resp))

	// Update State to Running
	inst.State = "running"
	return s.SaveInstance(ctx, inst)
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
	if err := client.Call("NodeRPC.StopAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "stop agent rpc succeeded", logattr.Response(resp))
	return nil
}

func (s *Scheduler) getNodeAddress(ctx context.Context, nodeID string) (string, error) {
	members := s.cluster.Members()
	for _, m := range members {
		if m.Name == nodeID {
			// Nodes broadcast their RPC endpoint in the api_addr tag;
			// the serf address/port is gossip, not RPC.
			if api := m.Tags["api_addr"]; api != "" {
				return api, nil
			}
			return "", fmt.Errorf("node %s has no api_addr tag", nodeID)
		}
	}
	return "", fmt.Errorf("node %s not found", nodeID)
}

// RunReconciler starts the periodic reconciliation loop
func (s *Scheduler) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "reconciler started")
	for {
		select {
		case <-ctx.Done():
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
