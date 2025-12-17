package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// ReconcileAgents ensures the actual state matches the desired state for all agents.
func (s *Scheduler) ReconcileAgents(ctx context.Context) error {
	specs, err := s.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("failed to list agent specs: %w", err)
	}

	for _, spec := range specs {
		if err := s.reconcileAgent(ctx, spec); err != nil {
			log.Printf("❌ Failed to reconcile agent %s: %v", spec.Id, err)
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
		if inst.State == "running" || inst.State == "pending" {
			active = append(active, inst)
		} else if inst.State == "failed" {
			// Handle failure: Restart/Reschedule
			log.Printf("⚠️ Instance %s failed. Triggering recovery...", inst.InstanceId)
			// For now, just remove it from active list so it gets replaced
			// In real impl, we might want to cleanup the old node first
			s.DeleteInstance(ctx, inst.InstanceId)
		}
	}

	currentCount := int32(len(active))
	desiredCount := spec.Replicas

	if currentCount < desiredCount {
		// Scale Up
		needed := int(desiredCount - currentCount)
		log.Printf("📈 Scaling up agent %s: need %d more instances", spec.Id, needed)
		for i := 0; i < needed; i++ {
			if err := s.createInstance(ctx, spec); err != nil {
				log.Printf("❌ Failed to create instance for %s: %v", spec.Id, err)
			}
		}
	} else if currentCount > desiredCount {
		// Scale Down
		excess := int(currentCount - desiredCount)
		log.Printf("📉 Scaling down agent %s: removing %d instances", spec.Id, excess)
		// Simple strategy: Remove newest (or random)
		// 'active' might not be sorted. Just pick last 'excess' elements.
		for i := 0; i < excess; i++ {
			inst := active[len(active)-1-i]
			if err := s.terminateInstance(ctx, inst); err != nil {
				log.Printf("❌ Failed to terminate instance %s: %v", inst.InstanceId, err)
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

	// 4. Trigger Start on Node (Async to avoid blocking Reconciler loop)
	go func() {
		if err := s.startAgentOnNode(context.Background(), nodeID, instance, spec); err != nil {
			log.Printf("❌ Failed to start agent %s on node %s: %v", instance.InstanceId, nodeID, err)
			// TODO: Mark instance as failed?
		}
	}()

	return nil
}

func (s *Scheduler) terminateInstance(ctx context.Context, inst *goblinv1.AgentInstance) error {
	// 1. Trigger Stop on Node
	if err := s.stopAgentOnNode(ctx, inst.NodeId, inst.InstanceId); err != nil {
		log.Printf("⚠️ Failed to stop agent on node (continuing cleanup): %v", err)
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
		log.Printf("⚠️ Failed to calculate usage: %v", err)
		usageMap = make(map[string]nodeUsage)
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
				AgentCount: u.jobCount, // We might need separate agent count vs job count
			},
		})
	}
	return candidates, nil
}

// Stubs for RPC calls (Phase 3.4 will replace these) -> Real Implementation Phase 3.5

func (s *Scheduler) startAgentOnNode(ctx context.Context, nodeID string, inst *goblinv1.AgentInstance, spec *goblinv1.AgentSpec) error {
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
	defer client.Close()

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

	log.Printf("📞 [RPC] Calling StartAgentInstance on %s (%s)...", nodeID, addr)
	var resp string
	if err := client.Call("NodeRPC.StartAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	log.Printf("✅ [RPC] Success: %s", resp)

	// Update State to Running
	inst.State = "running"
	return s.SaveInstance(ctx, inst)
}

func (s *Scheduler) stopAgentOnNode(ctx context.Context, nodeID, instanceID string) error {
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
	defer client.Close()

	payload := struct {
		InstanceID string
	}{
		InstanceID: instanceID,
	}

	log.Printf("📞 [RPC] Calling StopAgentInstance on %s (%s)...", nodeID, addr)
	var resp string
	if err := client.Call("NodeRPC.StopAgentInstance", &payload, &resp); err != nil {
		return fmt.Errorf("rpc call failed: %w", err)
	}

	log.Printf("✅ [RPC] Stop Success: %s", resp)
	return nil
}

func (s *Scheduler) getNodeAddress(ctx context.Context, nodeID string) (string, error) {
	members := s.cluster.Members()
	for _, m := range members {
		if m.Name == nodeID {
			// Prefer advertise address or tag
			return fmt.Sprintf("%s:%d", m.Addr, m.Port), nil
		}
	}
	return "", fmt.Errorf("node %s not found", nodeID)
}

// RunReconciler starts the periodic reconciliation loop
func (s *Scheduler) RunReconciler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Println("🔄 Reconciler started")
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Ensure we are leader before reconciling
			// if !s.consensus.IsLeader() { continue }
			if err := s.ReconcileAgents(ctx); err != nil {
				log.Printf("❌ Reconcile cycle failed: %v", err)
			}
		}
	}
}
