package supervisor

import (
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// TestMigrateAndSignalRPCMessages_RoundTrip pins the wire encoding for
// the three SchedulerRPC methods converted from JSON to protobuf in
// this batch: MigrateJob, MigrateInstance and SignalAgentInstance.
// These messages exist so buf breaking can see the fields, which is
// only true while they marshal and unmarshal as protobuf
// (GOBLIN-DIV-036).
func TestMigrateAndSignalRPCMessages_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		fill func() proto.Message // returns a fresh, empty instance of msg's type
	}{
		{
			name: "MigrateJobRequest",
			msg:  &goblinv1.MigrateJobRequest{JobId: "job-1", ToNode: "node-2"},
			fill: func() proto.Message { return &goblinv1.MigrateJobRequest{} },
		},
		{
			name: "MigrateJobResponse",
			msg:  &goblinv1.MigrateJobResponse{JobId: "job-1", ToNode: "node-2"},
			fill: func() proto.Message { return &goblinv1.MigrateJobResponse{} },
		},
		{
			// instance_id stays the canonical UUID string here, not the
			// raw bytes spec_uuid/instance_uuid carry elsewhere in this
			// schema - it is the field goblinctl already prints and
			// passes (goblin-typed-rpc-design.md id-representation note).
			name: "MigrateInstanceRequest",
			msg:  &goblinv1.MigrateInstanceRequest{InstanceId: "018f3b1a-0000-7000-8000-000000000001", ToNode: "node-2"},
			fill: func() proto.Message { return &goblinv1.MigrateInstanceRequest{} },
		},
		{
			name: "MigrateInstanceResponse",
			msg: &goblinv1.MigrateInstanceResponse{
				InstanceId: "018f3b1a-0000-7000-8000-000000000001",
				FromNode:   "node-1",
				ToNode:     "node-2",
			},
			fill: func() proto.Message { return &goblinv1.MigrateInstanceResponse{} },
		},
		{
			name: "SignalAgentInstanceRequest",
			msg:  &goblinv1.SignalAgentInstanceRequest{InstanceId: "018f3b1a-0000-7000-8000-000000000001", Signum: 15},
			fill: func() proto.Message { return &goblinv1.SignalAgentInstanceRequest{} },
		},
		{
			name: "SignalAgentInstanceResponse",
			msg: &goblinv1.SignalAgentInstanceResponse{
				Signum:     15,
				InstanceId: "018f3b1a-0000-7000-8000-000000000001",
				NodeId:     "node-1",
			},
			fill: func() proto.Message { return &goblinv1.SignalAgentInstanceResponse{} },
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
