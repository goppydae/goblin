package supervisor

import (
	"testing"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fixedTime anchors the GetEventsResponse case to a deterministic
// instant so the test does not depend on wall-clock time.
var fixedTime = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// TestReadOnlyRPCMessages_RoundTrip pins the wire encoding for the seven
// read-only SchedulerRPC methods converted from JSON to protobuf: these
// messages exist so buf breaking can see the fields, which is only true
// while they marshal and unmarshal as protobuf (GOBLIN-DIV-036).
func TestReadOnlyRPCMessages_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		fill func() proto.Message // returns a fresh, empty instance of msg's type
	}{
		{
			name: "ListJobsRequest",
			msg:  &goblinv1.ListJobsRequest{},
			fill: func() proto.Message { return &goblinv1.ListJobsRequest{} },
		},
		{
			name: "ListJobsResponse",
			msg: &goblinv1.ListJobsResponse{
				Jobs: []*goblinv1.JobInfo{
					{JobId: "job-1", AssignedNode: "node-1", AgentType: "sleeper", Status: "running"},
				},
			},
			fill: func() proto.Message { return &goblinv1.ListJobsResponse{} },
		},
		{
			name: "MembersRequest",
			msg:  &goblinv1.MembersRequest{},
			fill: func() proto.Message { return &goblinv1.MembersRequest{} },
		},
		{
			name: "MembersResponse",
			msg: &goblinv1.MembersResponse{
				Members: []*goblinv1.MemberInfo{
					{Name: "node-1", Addr: "10.0.0.1:7000", Status: "alive", Tags: map[string]string{"role": "leader"}, Leader: true},
				},
			},
			fill: func() proto.Message { return &goblinv1.MembersResponse{} },
		},
		{
			name: "ListGlobalAgentsRequest",
			msg:  &goblinv1.ListGlobalAgentsRequest{},
			fill: func() proto.Message { return &goblinv1.ListGlobalAgentsRequest{} },
		},
		{
			name: "ListGlobalAgentsResponse",
			msg: &goblinv1.ListGlobalAgentsResponse{
				Agents: []*goblinv1.AgentSpec{
					{SpecUuid: []byte("0123456789abcdef"), Name: "web", Type: "sleeper", Replicas: 3},
				},
			},
			fill: func() proto.Message { return &goblinv1.ListGlobalAgentsResponse{} },
		},
		{
			name: "ListLocalAgentsRequest",
			msg:  &goblinv1.ListLocalAgentsRequest{},
			fill: func() proto.Message { return &goblinv1.ListLocalAgentsRequest{} },
		},
		{
			name: "ListLocalAgentsResponse",
			msg: &goblinv1.ListLocalAgentsResponse{
				Agents: []*goblinv1.LocalAgentInfo{
					{Id: "local-1", Type: "sleeper", State: "running", Language: "go", UptimeNs: 123456789},
				},
			},
			fill: func() proto.Message { return &goblinv1.ListLocalAgentsResponse{} },
		},
		{
			name: "GetGlobalAgentRequest",
			msg:  &goblinv1.GetGlobalAgentRequest{AgentId: "web"},
			fill: func() proto.Message { return &goblinv1.GetGlobalAgentRequest{} },
		},
		{
			name: "GetGlobalAgentResponse",
			msg: &goblinv1.GetGlobalAgentResponse{
				Spec: &goblinv1.AgentSpec{SpecUuid: []byte("0123456789abcdef"), Name: "web", Type: "sleeper", Replicas: 3},
			},
			fill: func() proto.Message { return &goblinv1.GetGlobalAgentResponse{} },
		},
		{
			name: "GetEventsRequest",
			msg:  &goblinv1.GetEventsRequest{Cursor: 42},
			fill: func() proto.Message { return &goblinv1.GetEventsRequest{} },
		},
		{
			name: "GetEventsResponse",
			msg: &goblinv1.GetEventsResponse{
				Events: []*goblinv1.LogEvent{
					{Index: 43, Timestamp: timestamppb.New(fixedTime), Message: "agent web scaled to 3 replicas"},
				},
			},
			fill: func() proto.Message { return &goblinv1.GetEventsResponse{} },
		},
		{
			name: "ListAgentInstancesRequest",
			msg:  &goblinv1.ListAgentInstancesRequest{SpecId: "web"},
			fill: func() proto.Message { return &goblinv1.ListAgentInstancesRequest{} },
		},
		{
			name: "ListAgentInstancesResponse",
			msg: &goblinv1.ListAgentInstancesResponse{
				Instances: []*goblinv1.AgentInstance{
					{
						InstanceUuid: []byte("fedcba9876543210"),
						SpecUuid:     []byte("0123456789abcdef"),
						NodeId:       "node-1",
						State:        goblinv1.InstanceState_INSTANCE_STATE_RUNNING,
					},
				},
			},
			fill: func() proto.Message { return &goblinv1.ListAgentInstancesResponse{} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := proto.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := tc.fill()
			if err := proto.Unmarshal(raw, got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !proto.Equal(tc.msg, got) {
				t.Errorf("round trip mismatch:\n got  = %v\n want = %v", got, tc.msg)
			}
		})
	}
}
