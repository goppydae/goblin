package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/rpc"
	"reflect"
	"strings"
	"sync"
	"time"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LogEvent represents a single event in the history
type LogEvent struct {
	Index     uint64
	Timestamp time.Time
	Message   string
}

// SchedulerRPC exposes scheduler operations via RPC
type SchedulerRPC struct {
	scheduler  *scheduler.Scheduler
	membership interface{} // cluster.Membership
	consensus  *consensus.Consensus
	agentMgr   *gapiagentmgr.AgentManager // GAPI agent manager (optional)

	// Signal-path collaborators (nil outside a full supervisor).
	issuer      *capability.Issuer
	revocations *capability.Revocations
	members     memberTagLister

	// Migration collaborators (nil outside a full supervisor, in which
	// case MigrateInstance refuses rather than half-running).
	migrateNodes  *migration.RPCNodes
	migrateRaft   *migration.RaftProposer
	migrateLogger *slog.Logger

	eventsMu  sync.RWMutex
	events    []LogEvent
	lastIndex uint64
}

const maxEvents = 50

// AddEvent appends a regular log message to the history
func (s *SchedulerRPC) AddEvent(msg string) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()

	s.lastIndex++
	event := LogEvent{
		Index:     s.lastIndex,
		Timestamp: time.Now(),
		Message:   msg,
	}

	s.events = append(s.events, event)
	if len(s.events) > maxEvents {
		s.events = s.events[len(s.events)-maxEvents:]
	}
}

// GetEvents returns events occurring after the given cursor
func (s *SchedulerRPC) GetEvents(req *goblinv1.GetEventsRequest, resp *goblinv1.GetEventsResponse) error {
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()

	var result []*goblinv1.LogEvent
	for _, event := range s.events {
		if event.Index > req.GetCursor() {
			result = append(result, &goblinv1.LogEvent{
				Index:     event.Index,
				Timestamp: timestamppb.New(event.Timestamp),
				Message:   event.Message,
			})
		}
	}
	resp.Events = result
	return nil
}

// MigrateRequest contains parameters for job migration
type MigrateRequest struct {
	JobID  string
	ToNode string
}

// jobFromProto translates the RPC-facing Job message into the core
// scheduler's domain type, field for field. core/scheduler.Job predates
// protobuf and stays a plain Go struct (its storage encoding is JSON in
// the KV store); this is the one place the two shapes meet.
func jobFromProto(j *goblinv1.Job) *scheduler.Job {
	return &scheduler.Job{
		ID:            j.GetId(),
		AgentID:       j.GetAgentId(),
		AgentType:     j.GetAgentType(),
		AssignedNode:  j.GetAssignedNode(),
		Resources:     resourceReqFromProto(j.GetResources()),
		Constraints:   j.GetConstraints(),
		Requirements:  j.GetRequirements(),
		RestartPolicy: j.GetRestartPolicy(),
		Env:           j.GetEnv(),
	}
}

// resourceReqFromProto tolerates a nil message (unset field): the
// generated getters already do, so the zero ResourceReq falls out
// naturally rather than needing a special case.
func resourceReqFromProto(r *goblinv1.ResourceReq) scheduler.ResourceReq {
	return scheduler.ResourceReq{CPU: r.GetCpu(), Memory: r.GetMemory()}
}

// SubmitJob handles job submission via RPC.
func (s *SchedulerRPC) SubmitJob(req *goblinv1.SubmitJobRequest, resp *goblinv1.SubmitJobResponse) error {
	job := jobFromProto(req.GetJob())
	if _, err := s.authorize(capability.VerbJobSubmit, jobSubject(job.ID)); err != nil {
		return err
	}
	if err := s.scheduler.SubmitJob(context.Background(), job); err != nil {
		return err
	}
	resp.JobId = job.ID
	resp.AssignedNode = job.AssignedNode
	return nil
}

// DrainNode handles node draining via RPC.
func (s *SchedulerRPC) DrainNode(req *goblinv1.DrainNodeRequest, resp *goblinv1.DrainNodeResponse) error {
	nodeID := req.GetNodeId()
	if _, err := s.authorize(capability.VerbNodeDrain, nodeSubject(nodeID)); err != nil {
		return err
	}
	migratedJobs, err := s.scheduler.DrainNode(context.Background(), nodeID)
	if err != nil {
		return err
	}
	resp.MigratedJobIds = migratedJobs
	return nil
}

// MigrateJob handles job migration via RPC
func (s *SchedulerRPC) MigrateJob(req *MigrateRequest, resp *string) error {
	if _, err := s.authorize(capability.VerbJobMigrate, jobSubject(req.JobID)); err != nil {
		return err
	}
	if err := s.scheduler.MigrateJob(context.Background(), req.JobID, req.ToNode); err != nil {
		*resp = fmt.Sprintf("failed: %v", err)
		return err
	}
	*resp = fmt.Sprintf("job %s migrated to %s", req.JobID, req.ToNode)
	return nil
}

// ListJobs returns all jobs in the cluster
func (s *SchedulerRPC) ListJobs(req *goblinv1.ListJobsRequest, resp *goblinv1.ListJobsResponse) error {
	ctx := context.Background()

	// Scan all job assignments
	assignments, err := s.scheduler.Store().Scan(ctx, "default", "/jobs/assignments/")
	if err != nil {
		return fmt.Errorf("failed to scan assignments: %w", err)
	}

	var jobs []*goblinv1.JobInfo
	for key, jobIDBytes := range assignments {
		// Key format: /jobs/assignments/<node>/<job-id>
		parts := strings.Split(key, "/")
		if len(parts) < 5 {
			continue
		}

		nodeID := parts[3]
		jobID := string(jobIDBytes)

		// Fetch job spec for details
		specKey := fmt.Sprintf("/jobs/specs/%s", jobID)
		specData, found, _ := s.scheduler.Store().Get(ctx, "default", specKey)

		agentType := "unknown"
		if found {
			var job map[string]interface{}
			if err := json.Unmarshal(specData, &job); err == nil {
				if at, ok := job["agent_type"].(string); ok {
					agentType = at
				}
			}
		}

		jobs = append(jobs, &goblinv1.JobInfo{
			JobId:        jobID,
			AssignedNode: nodeID,
			AgentType:    agentType,
			Status:       "running",
		})
	}

	resp.Jobs = jobs
	return nil
}

// Members returns the list of cluster members
func (s *SchedulerRPC) Members(req *goblinv1.MembersRequest, resp *goblinv1.MembersResponse) error {
	leaderAddr := ""
	if s.consensus != nil {
		leaderAddr = s.consensus.Leader()
	}

	// Try type assertion - membership should be *cluster.Membership
	// But we'll use interface{} and reflection to avoid import cycle
	if s.membership != nil {
		// Use reflection to call Members() method
		method := reflect.ValueOf(s.membership).MethodByName("Members")
		if method.IsValid() {
			results := method.Call(nil)
			if len(results) > 0 {
				// Results[0] should be []serf.Member
				members := results[0]
				for i := 0; i < members.Len(); i++ {
					member := members.Index(i)

					// Extract Name and Status fields from serf.Member
					name := member.FieldByName("Name").String()
					statusVal := member.FieldByName("Status").Int()
					status := "unknown"
					switch statusVal {
					case 1: // StatusAlive
						status = "alive"
					case 2: // StatusLeft
						status = "left"
					case 3: // StatusFailed
						status = "failed"
					case 4: // StatusLeaving
						status = "leaving"
					}
					addr := member.FieldByName("Addr").Bytes() // Addr is likely net.IP slice []byte
					port := 0
					if portU := member.FieldByName("Port").Uint(); portU <= 65535 {
						port = int(portU)
					}

					// Need to format Addr
					ip := net.IP(addr)

					// Extract Tags
					tagsFunc := member.FieldByName("Tags")
					tags := make(map[string]string)
					iter := tagsFunc.MapRange()
					for iter.Next() {
						tags[iter.Key().String()] = iter.Value().String()
					}

					// Check Leadership: single-listener model - the raft
					// leader address IS the member's advertised address.
					memberAddr := fmt.Sprintf("%s:%d", ip.String(), port)
					isLeader := memberAddr == leaderAddr

					info := &goblinv1.MemberInfo{
						Name:   name,
						Addr:   memberAddr,
						Status: status,
						Tags:   tags,
						Leader: isLeader,
					}
					resp.Members = append(resp.Members, info)
				}
			}
		}
	}

	return nil
}

// PublishEvent publishes an event to the cluster via the EventBus.
// membership is interface{} on SchedulerRPC to avoid an import cycle,
// so UserEvent support is checked with a local interface rather than a
// concrete type.
func (s *SchedulerRPC) PublishEvent(req *goblinv1.PublishEventRequest, resp *goblinv1.PublishEventResponse) error {
	if _, err := s.authorize(capability.VerbEventPublish, topicSubject(req.GetTopic())); err != nil {
		return err
	}

	type eventPublisher interface {
		UserEvent(name string, payload []byte) error
	}

	publisher, ok := s.membership.(eventPublisher)
	if !ok {
		return fmt.Errorf("membership implementation does not support UserEvent")
	}
	if err := publisher.UserEvent(req.GetTopic(), req.GetPayload()); err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}
	resp.Topic = req.GetTopic()
	return nil
}

// Global Agent RPC Methods

// RegisterGlobalAgent registers a new global agent spec. spec_uuid is
// server-owned: RegisterAgent mints it from spec.name when unset. A
// caller-supplied spec_uuid is rejected rather than silently
// overwritten (design doc, "Server-owned fields must be rejected, not
// overwritten") so a client bug cannot masquerade as the minted
// identity.
func (s *SchedulerRPC) RegisterGlobalAgent(req *goblinv1.RegisterGlobalAgentRequest, resp *goblinv1.RegisterGlobalAgentResponse) error {
	spec := req.GetSpec()
	if spec == nil {
		return fmt.Errorf("%w: spec is required", ErrInvalidRequest)
	}
	if _, err := s.authorize(capability.VerbAgentRegister, specSubject(spec.GetName())); err != nil {
		return err
	}
	if len(spec.GetSpecUuid()) != 0 {
		return fmt.Errorf("%w: spec_uuid is server-owned (minted at registration) and must be left unset by the caller", ErrInvalidRequest)
	}
	if err := s.scheduler.RegisterAgent(context.Background(), spec); err != nil {
		return err
	}
	s.scheduler.KickReconcile()
	resp.SpecUuid = spec.SpecUuid
	resp.Name = spec.Name
	return nil
}

// ListAgentInstances returns the scheduler's instance records. SpecId
// accepts either the canonical spec UUID or the operator-facing name.
func (s *SchedulerRPC) ListAgentInstances(req *goblinv1.ListAgentInstancesRequest, resp *goblinv1.ListAgentInstancesResponse) error {
	specID := req.GetSpecId()
	if specID != "" {
		if _, err := ident.Parse(specID); err != nil {
			spec, gerr := s.scheduler.GetAgent(context.Background(), specID)
			if gerr != nil {
				return fmt.Errorf("resolve spec %q: %w", specID, gerr)
			}
			specID = ident.String(spec.SpecUuid)
		}
	}
	instances, err := s.scheduler.ListInstances(context.Background(), specID)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	resp.Instances = instances
	return nil
}

// ListGlobalAgents returns all global agents
func (s *SchedulerRPC) ListGlobalAgents(req *goblinv1.ListGlobalAgentsRequest, resp *goblinv1.ListGlobalAgentsResponse) error {
	specs, err := s.scheduler.ListAgents(context.Background())
	if err != nil {
		return err
	}
	resp.Agents = specs
	return nil
}

// GetGlobalAgent returns a specific global agent by ID
func (s *SchedulerRPC) GetGlobalAgent(req *goblinv1.GetGlobalAgentRequest, resp *goblinv1.GetGlobalAgentResponse) error {
	spec, err := s.scheduler.GetAgent(context.Background(), req.GetAgentId())
	if err != nil {
		return err
	}
	resp.Spec = spec
	return nil
}

// ScaleAgent updates the replica count for an agent.
func (s *SchedulerRPC) ScaleAgent(req *goblinv1.ScaleAgentRequest, resp *goblinv1.ScaleAgentResponse) error {
	if _, err := s.authorize(capability.VerbAgentScale, specSubject(req.GetAgentId())); err != nil {
		return err
	}
	spec, err := s.scheduler.GetAgent(context.Background(), req.GetAgentId())
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}
	spec.Replicas = req.GetReplicas()
	if err := s.scheduler.RegisterAgent(context.Background(), spec); err != nil {
		return fmt.Errorf("failed to update agent: %w", err)
	}
	s.scheduler.KickReconcile()
	resp.SpecUuid = spec.SpecUuid
	resp.Replicas = spec.Replicas
	return nil
}

// DeleteGlobalAgent removes a global agent spec.
func (s *SchedulerRPC) DeleteGlobalAgent(req *goblinv1.DeleteGlobalAgentRequest, resp *goblinv1.DeleteGlobalAgentResponse) error {
	agentID := req.GetAgentId()
	if _, err := s.authorize(capability.VerbAgentDelete, specSubject(agentID)); err != nil {
		return err
	}
	spec, err := s.scheduler.GetAgent(context.Background(), agentID)
	if err != nil {
		return err
	}
	if err := s.scheduler.DeleteAgent(context.Background(), agentID); err != nil {
		return err
	}
	s.scheduler.KickReconcile()
	resp.SpecUuid = spec.SpecUuid
	resp.Name = spec.Name
	return nil
}

// ListLocalAgents returns agents managed by the local GAPI agent manager
func (s *SchedulerRPC) ListLocalAgents(req *goblinv1.ListLocalAgentsRequest, resp *goblinv1.ListLocalAgentsResponse) error {
	if s.agentMgr == nil {
		// Local agents not enabled
		return nil
	}

	// Get all agents from GAPI agent manager
	agents := s.agentMgr.All()

	for _, agent := range agents {
		info := &goblinv1.LocalAgentInfo{
			Id:       agent.ID(),
			Type:     agent.Type(),
			Language: agent.Lang(),
			State:    agent.Controller().State(),
			UptimeNs: int64(agent.(interface{ Uptime() time.Duration }).Uptime()),
		}

		resp.Agents = append(resp.Agents, info)
	}

	return nil
}

// RegisterSchedulerRPC registers the scheduler RPC service
func RegisterSchedulerRPC(sched *scheduler.Scheduler, membership interface{}, consensus *consensus.Consensus) error {
	return rpc.Register(&SchedulerRPC{scheduler: sched, membership: membership, consensus: consensus})
}
