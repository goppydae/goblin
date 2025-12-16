package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/rpc"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/scheduler"
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
	if err := s.scheduler.SubmitJob(context.Background(), job); err != nil {
		*resp = fmt.Sprintf("failed: %v", err)
		return err
	}
	*resp = fmt.Sprintf("job %s submitted successfully", job.ID)
	return nil
}

// DrainNode handles node draining via RPC
func (s *SchedulerRPC) DrainNode(nodeID *string, resp *[]string) error {
	migratedJobs, err := s.scheduler.DrainNode(context.Background(), *nodeID)
	if err != nil {
		return err
	}
	*resp = migratedJobs
	return nil
}

// MigrateJob handles job migration via RPC
func (s *SchedulerRPC) MigrateJob(req *MigrateRequest, resp *string) error {
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
	// Define interface for membership with Members() method
	type membershipInterface interface {
		Members() []interface{}
	}

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
					port := int(member.FieldByName("Port").Uint())

					// Need to format Addr
					ip := net.IP(addr)

					// Extract Tags
					tagsFunc := member.FieldByName("Tags")
					tags := make(map[string]string)
					iter := tagsFunc.MapRange()
					for iter.Next() {
						tags[iter.Key().String()] = iter.Value().String()
					}

					// Check Leadership
					isLeader := false
					if raftAddr, ok := tags["raft_addr"]; ok {
						if raftAddr == leaderAddr {
							isLeader = true
						}
					}

					info := MemberInfo{
						Name:   name,
						Addr:   fmt.Sprintf("%s:%d", ip.String(), port),
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

// RegisterSchedulerRPC registers the scheduler RPC service
func RegisterSchedulerRPC(sched *scheduler.Scheduler, membership interface{}, consensus *consensus.Consensus) error {
	return rpc.Register(&SchedulerRPC{scheduler: sched, membership: membership, consensus: consensus})
}
