package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	gapiclock "github.com/goppydae/gapi/core/clock"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
)

// RPCClient defines a generic RPC client
type RPCClient interface {
	Call(serviceMethod string, args interface{}, reply interface{}) error
	Close() error
}

// ClientFactory creates an RPC client for a given address
type ClientFactory func(addr string) (RPCClient, error)

// KVStore defines the interface for storage operations: the generic KV
// half plus the typed instance-lifecycle half (admission and
// transitions are Raft commands validated by the FSM, never plain
// writes).
type KVStore interface {
	Set(ctx context.Context, namespace, key string, value []byte) error
	Get(ctx context.Context, namespace, key string) ([]byte, bool, error)
	Scan(ctx context.Context, namespace, prefix string) (map[string][]byte, error)
	Delete(ctx context.Context, namespace, key string) error

	Admit(ctx context.Context, specUUID, instanceUUID []byte, nodeID string) error
	TransitionInstance(ctx context.Context, instanceUUID []byte, to goblinv1.InstanceState, reason string) error
	SignalInstance(ctx context.Context, req *goblinv1.SignalRequest) (string, error)
	GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, bool, error)
	ListInstances(ctx context.Context) ([]*goblinv1.AgentInstance, error)
}

// Cluster defines the interface for cluster membership operations
type Cluster interface {
	Members() []serf.Member
}

type Scheduler struct {
	store   KVStore
	cluster Cluster
	bus     eventbus.EventBus

	clientFactory ClientFactory

	placement *PlacementEngine

	// isLeader gates reconciliation: only the Raft leader writes; followers
	// read and react (review R7). nil means standalone mode (always leader).
	isLeader func() bool

	// clk drives heartbeat staleness; tests inject a MockClock.
	clk gapiclock.Clock
	// hbMu guards heartbeats and leadSince.
	hbMu       sync.Mutex
	heartbeats map[string]heartbeat
	// pendingSince tracks when a pending instance was first observed,
	// so dispatch losses cannot hold replica slots forever.
	pendingSince map[string]time.Time
	leadSince    time.Time
	// locators is the gossip-fed runtime location map (LWW on HLC).
	locators map[string]Locator
	// reconcileKick requests an immediate reconcile pass (buffered 1).
	reconcileKick chan struct{}
}

// NewScheduler creates a new Scheduler instance. isLeader is the leadership
// predicate reconciliation is gated on (pass consensus.IsLeader in cluster
// deployments); nil means standalone mode, in which every reconcile runs.
func NewScheduler(store KVStore, c Cluster, bus eventbus.EventBus, isLeader func() bool, clientFactory ClientFactory) *Scheduler {
	s := &Scheduler{
		store:         store,
		cluster:       c,
		bus:           bus,
		clientFactory: clientFactory,
		isLeader:      isLeader,
		clk:           gapiclock.RealClock{},
		heartbeats:    make(map[string]heartbeat),
		locators:      make(map[string]Locator),
		pendingSince:  make(map[string]time.Time),
		reconcileKick: make(chan struct{}, 1),
	}
	s.placement = NewPlacementEngine()
	return s
}

// leading reports whether this node may run reconciliation writes.
func (s *Scheduler) leading() bool {
	return s.isLeader == nil || s.isLeader()
}

// Store returns the underlying KV store
func (s *Scheduler) Store() KVStore {
	return s.store
}

// Schedule selects a node for the job based on strategy.
func (s *Scheduler) Schedule(job *Job, strategy Strategy) (string, error) {
	members := s.cluster.Members()
	if len(members) == 0 {
		return "", fmt.Errorf("no nodes available in cluster")
	}

	// 1. Filter nodes by Liveness and Constraints
	var candidates []serf.Member
	for _, m := range members {
		if m.Status != 1 { // MemberStatusAlive
			continue
		}
		if !checkConstraints(m, job.Constraints) {
			continue
		}
		candidates = append(candidates, m)
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no suitable nodes found matching constraints")
	}

	// 2. Filter nodes by Resource Capacity
	// Calculate current usage for all candidates first.
	// Map: nodeID -> {usedCPU, usedMem}
	usageMap, err := s.calculateUsage(context.Background(), candidates)
	if err != nil {
		// Log warning but proceed? Or fail? Proceeds with zero usage is risky.
		// For MVP, if KV fails, we might just assume empty usage or fail.
		return "", fmt.Errorf("failed to calculate cluster usage: %w", err)
	}

	var capableNodes []serf.Member
	for _, m := range candidates {
		if hasCapacity(m, usageMap[m.Name], job.Resources) {
			capableNodes = append(capableNodes, m)
		}
	}

	if len(capableNodes) == 0 {
		return "", fmt.Errorf("insufficient capacity in cluster for job %s", job.ID)
	}

	// 3. Apply Strategy
	switch strategy {
	case StrategyRandom, StrategyRoundRobin:
		// Use local random source instead of global Seed (deprecated)
		src := rand.NewSource(time.Now().UnixNano())
		r := rand.New(src)

		// Shuffle nodes
		r.Shuffle(len(capableNodes), func(i, j int) {
			capableNodes[i], capableNodes[j] = capableNodes[j], capableNodes[i]
		})
		return capableNodes[0].Name, nil

	case StrategyLeastLoaded:
		// Pick node with lowest % resource utilization (avg of cpu% and mem%)
		return selectLeastLoaded(capableNodes, usageMap), nil

	case StrategyBinPack:
		// Pick node with highest utilization that still fits the job
		return selectBinPack(capableNodes, usageMap), nil

	default:
		return "", fmt.Errorf("unknown strategy: %s", strategy)
	}
}

// Assign persists the assignment to the KV store.
func (s *Scheduler) Assign(ctx context.Context, jobID, nodeID string) error {
	// Store assignment mapping
	key := fmt.Sprintf("/jobs/assignments/%s/%s", nodeID, jobID)
	if err := s.store.Set(ctx, "default", key, []byte(jobID)); err != nil {
		return fmt.Errorf("assignment failed: %w", err)
	}

	if s.bus != nil {
		payload := map[string]interface{}{"job_id": jobID, "node_id": nodeID}
		// The assignment is already persisted; a publish failure must not
		// misreport the write, so it is logged rather than propagated.
		if err := s.bus.PublishLocal("system", "job.assigned", payload, []string{"job", jobID}); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish job.assigned failed", logattr.JobID(jobID), logattr.Err(err))
		}
	}
	return nil
}

// RegisterJob stores the job definition.
func (s *Scheduler) RegisterJob(ctx context.Context, job *Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("/jobs/specs/%s", job.ID)
	return s.store.Set(ctx, "default", key, data)
}

// Migrate moves a job from one node to another
func (s *Scheduler) Migrate(ctx context.Context, jobID, fromNode, toNode string) error {
	if err := s.Assign(ctx, jobID, toNode); err != nil {
		return fmt.Errorf("failed to assign to new node: %w", err)
	}

	key := fmt.Sprintf("/jobs/assignments/%s/%s", fromNode, jobID)
	if err := s.store.Delete(ctx, "default", key); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to cleanup old assignment", logattr.JobID(jobID), logattr.NodeID(fromNode), logattr.Err(err))
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "job migrated", logattr.JobID(jobID), logattr.From(fromNode), logattr.To(toNode))
	if s.bus != nil {
		payload := map[string]interface{}{"job_id": jobID, "from_node": fromNode, "to_node": toNode}
		// The migration is already persisted; a publish failure must not
		// misreport the write, so it is logged rather than propagated.
		if err := s.bus.PublishLocal("system", "job.migrated", payload, []string{"job", jobID}); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish job.migrated failed", logattr.JobID(jobID), logattr.Err(err))
		}
	}
	return nil
}

// HandleNodeFailure reschedules all jobs from a failed node
func (s *Scheduler) HandleNodeFailure(ctx context.Context, failedNodeID string) error {
	prefix := fmt.Sprintf("/jobs/assignments/%s/", failedNodeID)
	assignments, err := s.store.Scan(ctx, "default", prefix)
	if err != nil {
		return err
	}

	if len(assignments) == 0 {
		return nil
	}

	slog.Default().LogAttrs(ctx, slog.LevelWarn, "node failed, rescheduling jobs", logattr.NodeID(failedNodeID), logattr.Count(len(assignments)))

	for _, jobIDBytes := range assignments {
		jobID := string(jobIDBytes)

		// Fetch job spec to know resources
		specKey := fmt.Sprintf("/jobs/specs/%s", jobID)
		specData, found, err := s.store.Get(ctx, "default", specKey)
		var job *Job
		if err == nil && found {
			_ = json.Unmarshal(specData, &job)
		}
		if job == nil {
			job = &Job{ID: jobID} // Fallback to empty spec if missing
		}

		newNode, err := s.Schedule(job, StrategyLeastLoaded) // Use smart strategy
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to find new node for job", logattr.JobID(jobID), logattr.Err(err))
			continue
		}

		if err := s.Migrate(ctx, jobID, failedNodeID, newNode); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "migration failed for job", logattr.JobID(jobID), logattr.Err(err))
		}
	}
	return nil
}

// Internal Helpers

func checkConstraints(m serf.Member, constraints map[string]string) bool {
	for k, v := range constraints {
		if mVal, ok := m.Tags[k]; !ok || mVal != v {
			return false
		}
	}
	return true
}

type nodeUsage struct {
	cpuUsed  float64
	memUsed  int64
	cpuTotal float64
	memTotal int64
	jobCount int
}

func (s *Scheduler) calculateUsage(ctx context.Context, nodes []serf.Member) (map[string]nodeUsage, error) {
	// This is expensive: scan all assignments then fetch specs.
	// Optimally: Maintain /stats/usage/nodeID counters in logic.
	// For MVP: Scan all assignments.

	usage := make(map[string]nodeUsage)

	// Initialize totals from tags
	for _, m := range nodes {
		cpu, _ := strconv.ParseFloat(m.Tags["cpu"], 64)
		mem, _ := strconv.ParseInt(m.Tags["memory"], 10, 64)
		usage[m.Name] = nodeUsage{cpuTotal: cpu, memTotal: mem}
	}

	// Scan all assignments: /jobs/assignments/ -> key is /jobs/assignments/<node>/<job>
	assignments, err := s.store.Scan(ctx, "default", "/jobs/assignments/")
	if err != nil {
		return nil, err
	}

	for key, jobIDBytes := range assignments {
		// Key: /jobs/assignments/<node>/<job>
		parts := strings.Split(key, "/")
		if len(parts) < 4 {
			continue
		}
		nodeID := parts[3]
		jobID := string(jobIDBytes)

		u, ok := usage[nodeID]
		if !ok {
			continue // Node not in our candidate list (maybe dead)
		}

		// Fetch job spec
		specKey := fmt.Sprintf("/jobs/specs/%s", jobID)
		specData, found, _ := s.store.Get(ctx, "default", specKey)
		if found {
			var job Job
			if err := json.Unmarshal(specData, &job); err == nil {
				u.cpuUsed += job.Resources.CPU
				u.memUsed += job.Resources.Memory
			}
		}
		u.jobCount++
		usage[nodeID] = u
	}
	return usage, nil
}

func hasCapacity(m serf.Member, u nodeUsage, req ResourceReq) bool {
	if u.cpuTotal > 0 && (u.cpuUsed+req.CPU > u.cpuTotal) {
		return false
	}
	if u.memTotal > 0 && (u.memUsed+req.Memory > u.memTotal) {
		return false
	}
	return true
}

func selectLeastLoaded(nodes []serf.Member, usage map[string]nodeUsage) string {
	bestNode := ""
	minScore := 101.0 // > 100%

	for _, n := range nodes {
		u := usage[n.Name]
		cpuPct := 0.0
		if u.cpuTotal > 0 {
			cpuPct = u.cpuUsed / u.cpuTotal
		}
		memPct := 0.0
		if u.memTotal > 0 {
			memPct = float64(u.memUsed) / float64(u.memTotal)
		}
		score := (cpuPct + memPct) / 2.0 // Simple avg

		if score < minScore {
			minScore = score
			bestNode = n.Name
		}
	}
	return bestNode
}

func selectBinPack(nodes []serf.Member, usage map[string]nodeUsage) string {
	bestNode := ""
	maxScore := -1.0

	for _, n := range nodes {
		u := usage[n.Name]
		cpuPct := 0.0
		if u.cpuTotal > 0 {
			cpuPct = u.cpuUsed / u.cpuTotal
		}
		memPct := 0.0
		if u.memTotal > 0 {
			memPct = float64(u.memUsed) / float64(u.memTotal)
		}
		score := (cpuPct + memPct) / 2.0

		if score > maxScore {
			maxScore = score
			bestNode = n.Name
		}
	}
	return bestNode
}

// SubmitJob registers and schedules a job
func (s *Scheduler) SubmitJob(ctx context.Context, job *Job) error {
	// Register the job spec
	if err := s.RegisterJob(ctx, job); err != nil {
		return fmt.Errorf("failed to register job: %w", err)
	}

	// Schedule the job
	nodeID, err := s.Schedule(job, StrategyLeastLoaded)
	if err != nil {
		return fmt.Errorf("failed to schedule job: %w", err)
	}

	// Assign to the selected node
	if err := s.Assign(ctx, job.ID, nodeID); err != nil {
		return fmt.Errorf("failed to assign job: %w", err)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "job scheduled", logattr.JobID(job.ID), logattr.NodeID(nodeID))
	if s.bus != nil {
		payload := map[string]interface{}{"job_id": job.ID, "node_id": nodeID}
		// The job is already registered and assigned; a publish failure
		// must not misreport the submission, so it is logged rather than
		// propagated.
		if err := s.bus.PublishLocal("system", "job.submitted", payload, []string{"job", job.ID}); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish job.submitted failed", logattr.JobID(job.ID), logattr.Err(err))
		}
	}
	return nil
}

// DrainNode migrates all jobs from a node to other nodes
func (s *Scheduler) DrainNode(ctx context.Context, nodeID string) ([]string, error) {
	prefix := fmt.Sprintf("/jobs/assignments/%s/", nodeID)
	assignments, err := s.store.Scan(ctx, "default", prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to scan assignments: %w", err)
	}

	if len(assignments) == 0 {
		return []string{}, nil
	}

	var migratedJobs []string
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "draining jobs from node", logattr.Count(len(assignments)), logattr.NodeID(nodeID))

	for _, jobIDBytes := range assignments {
		jobID := string(jobIDBytes)

		// Fetch job spec
		specKey := fmt.Sprintf("/jobs/specs/%s", jobID)
		specData, found, err := s.store.Get(ctx, "default", specKey)
		var job *Job
		if err == nil && found {
			_ = json.Unmarshal(specData, &job)
		}
		if job == nil {
			job = &Job{ID: jobID}
		}

		// Schedule to a different node
		newNode, err := s.Schedule(job, StrategyLeastLoaded)
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "failed to find new node for job", logattr.JobID(jobID), logattr.Err(err))
			continue
		}

		// Don't migrate to the same node
		if newNode == nodeID {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "no alternative node available for job", logattr.JobID(jobID))
			continue
		}

		if err := s.Migrate(ctx, jobID, nodeID, newNode); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "migration failed for job", logattr.JobID(jobID), logattr.Err(err))
			continue
		}

		migratedJobs = append(migratedJobs, jobID)
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "drained jobs from node", logattr.Count(len(migratedJobs)), logattr.NodeID(nodeID))
	return migratedJobs, nil
}

// MigrateJob moves a specific job to a target node
func (s *Scheduler) MigrateJob(ctx context.Context, jobID, toNode string) error {
	// Find current assignment
	prefix := "/jobs/assignments/"
	assignments, err := s.store.Scan(ctx, "default", prefix)
	if err != nil {
		return fmt.Errorf("failed to scan assignments: %w", err)
	}

	var fromNode string
	for key, val := range assignments {
		if string(val) == jobID {
			parts := strings.Split(key, "/")
			if len(parts) >= 4 {
				fromNode = parts[3]
				break
			}
		}
	}

	if fromNode == "" {
		return fmt.Errorf("job %s not found in any assignment", jobID)
	}

	if fromNode == toNode {
		return fmt.Errorf("job %s is already on node %s", jobID, toNode)
	}

	return s.Migrate(ctx, jobID, fromNode, toNode)
}
