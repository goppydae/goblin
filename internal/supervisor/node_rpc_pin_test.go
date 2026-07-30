package supervisor

import (
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// TestNodeRPCMessages_RoundTrip pins the wire encoding for the twelve
// NodeRPC messages added in batch D: NodeStartAgentInstanceRequest/Response,
// NodeStopAgentInstanceRequest/Response, NodeSignalAgentInstanceRequest/Response,
// NodeCheckpointAgentInstanceRequest/Response, NodeRestoreAgentInstanceRequest/Response,
// NodePullCheckpointRequest/Response. These messages exist so buf breaking can
// see the fields, which is only true while they marshal and unmarshal as
// protobuf (GOBLIN-DIV-036).
func TestNodeRPCMessages_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		fill func() proto.Message // returns a fresh, empty instance of msg's type
	}{
		{
			name: "NodeStartAgentInstanceRequest",
			msg: &goblinv1.NodeStartAgentInstanceRequest{
				InstanceId: "inst-1",
				Spec: &goblinv1.AgentSpec{
					SpecUuid: []byte("0123456789abcdef"),
					Name:     "agent-1",
					Type:     "sleeper",
					Replicas: 1,
				},
			},
			fill: func() proto.Message { return &goblinv1.NodeStartAgentInstanceRequest{} },
		},
		{
			name: "NodeStartAgentInstanceResponse",
			msg:  &goblinv1.NodeStartAgentInstanceResponse{InstanceId: "inst-1"},
			fill: func() proto.Message { return &goblinv1.NodeStartAgentInstanceResponse{} },
		},
		{
			name: "NodeStopAgentInstanceRequest",
			msg:  &goblinv1.NodeStopAgentInstanceRequest{InstanceId: "inst-1"},
			fill: func() proto.Message { return &goblinv1.NodeStopAgentInstanceRequest{} },
		},
		{
			name: "NodeStopAgentInstanceResponse",
			msg: &goblinv1.NodeStopAgentInstanceResponse{
				InstanceId:     "inst-1",
				AlreadyStopped: true,
			},
			fill: func() proto.Message { return &goblinv1.NodeStopAgentInstanceResponse{} },
		},
		{
			name: "NodeSignalAgentInstanceRequest",
			msg: &goblinv1.NodeSignalAgentInstanceRequest{
				InstanceId: "inst-1",
				Signum:     9,
			},
			fill: func() proto.Message { return &goblinv1.NodeSignalAgentInstanceRequest{} },
		},
		{
			name: "NodeSignalAgentInstanceResponse",
			msg: &goblinv1.NodeSignalAgentInstanceResponse{
				InstanceId: "inst-1",
				Signum:     9,
			},
			fill: func() proto.Message { return &goblinv1.NodeSignalAgentInstanceResponse{} },
		},
		{
			name: "NodeCheckpointAgentInstanceRequest",
			msg: &goblinv1.NodeCheckpointAgentInstanceRequest{
				InstanceId:   "inst-1",
				InstanceUuid: []byte("fedcba9876543210"),
				Epoch:        42,
			},
			fill: func() proto.Message { return &goblinv1.NodeCheckpointAgentInstanceRequest{} },
		},
		{
			name: "NodeCheckpointAgentInstanceResponse",
			msg:  &goblinv1.NodeCheckpointAgentInstanceResponse{ImageDir: "/var/lib/agent/images/inst-1"},
			fill: func() proto.Message { return &goblinv1.NodeCheckpointAgentInstanceResponse{} },
		},
		{
			name: "NodeRestoreAgentInstanceRequest",
			msg: &goblinv1.NodeRestoreAgentInstanceRequest{
				InstanceId:   "inst-1",
				InstanceUuid: []byte("fedcba9876543210"),
				Epoch:        42,
				Spec: &goblinv1.AgentSpec{
					SpecUuid: []byte("0123456789abcdef"),
					Name:     "agent-1",
					Type:     "sleeper",
					Replicas: 1,
				},
			},
			fill: func() proto.Message { return &goblinv1.NodeRestoreAgentInstanceRequest{} },
		},
		{
			name: "NodeRestoreAgentInstanceResponse",
			msg:  &goblinv1.NodeRestoreAgentInstanceResponse{InstanceId: "inst-1"},
			fill: func() proto.Message { return &goblinv1.NodeRestoreAgentInstanceResponse{} },
		},
		{
			name: "NodePullCheckpointRequest",
			msg: &goblinv1.NodePullCheckpointRequest{
				InstanceId:   "inst-1",
				InstanceUuid: []byte("fedcba9876543210"),
				Epoch:        42,
				SourceAddr:   "192.168.1.1:9999",
				Token:        []byte("auth-token"),
			},
			fill: func() proto.Message { return &goblinv1.NodePullCheckpointRequest{} },
		},
		{
			name: "NodePullCheckpointResponse",
			msg:  &goblinv1.NodePullCheckpointResponse{ImageDir: "/var/lib/agent/images/inst-1"},
			fill: func() proto.Message { return &goblinv1.NodePullCheckpointResponse{} },
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
