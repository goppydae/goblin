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
	"google.golang.org/protobuf/proto"
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

// GetEventsRequest defines the cursor for fetching events
type GetEventsRequest struct {
	Cursor uint64
}

// GetEvents returns events occurring after the given cursor
func (s *SchedulerRPC) GetEvents(req *GetEventsRequest, resp *[]LogEvent) error {
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()

	var result []LogEvent
	for _, event := range s.events {
		if event.Index > req.Cursor {
			result = append(result, event)
		}
	}
	*resp = result
	return nil
}

// MigrateRequest contains parameters for job migration
type MigrateRequest struct {
	JobID  string
	ToNode string
}

// SubmitJob handles job submission via RPC
func (s *SchedulerRPC) SubmitJob(job *scheduler.Job, resp *string) error {
	if _, err := s.authorize(capability.VerbJobSubmit, jobSubject(job.ID)); err != nil {
		return err
	}
	if err := s.scheduler.SubmitJob(context.Background(), job); err != nil {
		*resp = fmt.Sprintf("failed: %v", err)
		return err
	}
	*resp = fmt.Sprintf("job %s submitted successfully", job.ID)
	return nil
}

// DrainNode handles node draining via RPC
func (s *SchedulerRPC) DrainNode(nodeID *string, resp *[]string) error {
	if _, err := s.authorize(capability.VerbNodeDrain, nodeSubject(*nodeID)); err != nil {
		return err
	}
	migratedJobs, err := s.scheduler.DrainNode(context.Background(), *nodeID)
	if err != nil {
		return err
	}
	*resp = migratedJobs
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

// JobInfo contains information about a scheduled job
type JobInfo struct {
	JobID        string
	AssignedNode string
	AgentType    string
	Status       string
}

// ListJobs returns all jobs in the cluster
func (s *SchedulerRPC) ListJobs(req *struct{}, resp *[]JobInfo) error {
	ctx := context.Background()

	// Scan all job assignments
	assignments, err := s.scheduler.Store().Scan(ctx, "default", "/jobs/assignments/")
	if err != nil {
		return fmt.Errorf("failed to scan assignments: %w", err)
	}

	var jobs []JobInfo
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

		jobs = append(jobs, JobInfo{
			JobID:        jobID,
			AssignedNode: nodeID,
			AgentType:    agentType,
			Status:       "running",
		})
	}

	*resp = jobs
	return nil
}

// MemberInfo represents cluster member information
type MemberInfo struct {
	Name   string
	Addr   string
	Status string
	Tags   map[string]string
	Leader bool
}

// Members returns the list of cluster members
func (s *SchedulerRPC) Members(req *struct{}, resp *[]MemberInfo) error {
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

					info := MemberInfo{
						Name:   name,
						Addr:   memberAddr,
						Status: status,
						Tags:   tags,
						Leader: isLeader,
					}
					*resp = append(*resp, info)
				}
			}
		}
	}

	return nil
}

// PublishRequest defines the payload for publishing events
type PublishRequest struct {
	Topic   string
	Payload []byte // JSON payload
}

// PublishEvent publishes an event to the cluster via the EventBus
func (s *SchedulerRPC) PublishEvent(req *PublishRequest, resp *string) error {
	if _, err := s.authorize(capability.VerbEventPublish, topicSubject(req.Topic)); err != nil {
		return err
	}
	// Forward to membership (Serf) UserEvent for now, or preferably use the EventBus directly
	// The EventBus wraps Serf, so we should publish "user" type events.
	// However, SchedulerRPC has access to s.membership (interface{}) and s.consensus.
	// It doesn't hold the 'bus' explicitly in the struct definition in supervisor.go,
	// but supervisor.go initializes it with `membership`.
	// Wait, `membership` in SchedulerRPC is `interface{}` to avoid cycles.
	// To use UserEvent, we need to cast it or add UserEvent to the interface.

	// Let's check if we can cast to an interface with UserEvent
	type eventPublisher interface {
		UserEvent(name string, payload []byte) error
	}

	if publisher, ok := s.membership.(eventPublisher); ok {
		// Topic needs to be namespaced for Serf if we are mimicking `goblinctl publish`
		// which usually sends user events.
		// The CLI previously did: client.UserEvent(topic, payload, false)
		if err := publisher.UserEvent(req.Topic, req.Payload); err != nil {
			*resp = fmt.Sprintf("failed to publish: %v", err)
			return err
		}
		*resp = fmt.Sprintf("event '%s' published", req.Topic)
		return nil
	}

	*resp = "membership does not support UserEvent"
	return fmt.Errorf("membership implementation does not support UserEvent")
}

// LocalAgentInfo represents a local GAPI agent
type LocalAgentInfo struct {
	ID       string
	Type     string
	State    string
	Language string
	Uptime   int64 // nanoseconds
}

// Global Agent RPC Methods

// RegisterGlobalAgent registers a new global agent
func (s *SchedulerRPC) RegisterGlobalAgent(spec *goblinv1.AgentSpec, resp *string) error {
	if _, err := s.authorize(capability.VerbAgentRegister, specSubject(spec.Name)); err != nil {
		return err
	}
	if err := s.scheduler.RegisterAgent(context.Background(), spec); err != nil {
		*resp = fmt.Sprintf("failed: %v", err)
		return err
	}
	s.scheduler.KickReconcile()
	*resp = fmt.Sprintf("agent %s registered successfully (uuid %s)", spec.Name, ident.String(spec.SpecUuid))
	return nil
}

// ListAgentInstancesRequest filters instance listing; empty SpecID
// returns every instance.
type ListAgentInstancesRequest struct {
	SpecID string
}

// ListAgentInstances returns the scheduler's instance records. SpecID
// accepts either the canonical spec UUID or the operator-facing name.
func (s *SchedulerRPC) ListAgentInstances(req *ListAgentInstancesRequest, resp *[]*goblinv1.AgentInstance) error {
	specID := req.SpecID
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
	*resp = instances
	return nil
}

// ListGlobalAgents returns all global agents
func (s *SchedulerRPC) ListGlobalAgents(req *struct{}, resp *[]*goblinv1.AgentSpec) error {
	specs, err := s.scheduler.ListAgents(context.Background())
	if err != nil {
		return err
	}
	*resp = specs
	return nil
}

// GetGlobalAgent returns a specific global agent by ID
func (s *SchedulerRPC) GetGlobalAgent(agentID *string, resp *goblinv1.AgentSpec) error {
	spec, err := s.scheduler.GetAgent(context.Background(), *agentID)
	if err != nil {
		return err
	}
	proto.Merge(resp, spec)
	return nil
}

// ScaleAgentRequest defines the payload for scaling an agent
type ScaleAgentRequest struct {
	AgentID  string
	Replicas int32
}

// ScaleAgent updates the replica count for an agent
func (s *SchedulerRPC) ScaleAgent(req *ScaleAgentRequest, resp *string) error {
	if _, err := s.authorize(capability.VerbAgentScale, specSubject(req.AgentID)); err != nil {
		return err
	}
	// 1. Get existing spec
	spec, err := s.scheduler.GetAgent(context.Background(), req.AgentID)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// 2. Update replicas
	spec.Replicas = req.Replicas

	// 3. Update spec (Register overwrites)
	if err := s.scheduler.RegisterAgent(context.Background(), spec); err != nil {
		return fmt.Errorf("failed to update agent: %w", err)
	}

	s.scheduler.KickReconcile()
	*resp = fmt.Sprintf("agent %s scaled to %d replicas", req.AgentID, req.Replicas)
	return nil
}

// DeleteGlobalAgent removes a global agent
func (s *SchedulerRPC) DeleteGlobalAgent(agentID *string, resp *string) error {
	if _, err := s.authorize(capability.VerbAgentDelete, specSubject(*agentID)); err != nil {
		return err
	}
	if err := s.scheduler.DeleteAgent(context.Background(), *agentID); err != nil {
		*resp = fmt.Sprintf("failed: %v", err)
		return err
	}
	s.scheduler.KickReconcile()
	*resp = fmt.Sprintf("agent %s deleted", *agentID)
	return nil
}

// ListLocalAgents returns agents managed by the local GAPI agent manager
func (s *SchedulerRPC) ListLocalAgents(req *struct{}, resp *[]LocalAgentInfo) error {
	if s.agentMgr == nil {
		// Local agents not enabled
		*resp = []LocalAgentInfo{}
		return nil
	}

	// Get all agents from GAPI agent manager
	agents := s.agentMgr.All()

	for _, agent := range agents {
		info := LocalAgentInfo{
			ID:       agent.ID(),
			Type:     agent.Type(),
			Language: agent.Lang(),
			State:    agent.Controller().State(),
			Uptime:   int64(agent.(interface{ Uptime() time.Duration }).Uptime()),
		}

		*resp = append(*resp, info)
	}

	return nil
}

// RegisterSchedulerRPC registers the scheduler RPC service
func RegisterSchedulerRPC(sched *scheduler.Scheduler, membership interface{}, consensus *consensus.Consensus) error {
	return rpc.Register(&SchedulerRPC{scheduler: sched, membership: membership, consensus: consensus})
}
