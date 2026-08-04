// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// TestMutatingRPCMessages_RoundTrip pins the wire encoding for the five
// mutating SchedulerRPC methods converted from JSON to protobuf in this
// batch: RegisterGlobalAgent, DeleteGlobalAgent, SubmitJob, DrainNode
// and PublishEvent. These messages exist so buf breaking can see the
// fields, which is only true while they marshal and unmarshal as
// protobuf (GOBLIN-DIV-036).
func TestMutatingRPCMessages_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  proto.Message
		fill func() proto.Message // returns a fresh, empty instance of msg's type
	}{
		{
			name: "RegisterGlobalAgentRequest",
			msg: &goblinv1.RegisterGlobalAgentRequest{
				Spec: &goblinv1.AgentSpec{Name: "web", Type: "sleeper", Replicas: 3},
			},
			fill: func() proto.Message { return &goblinv1.RegisterGlobalAgentRequest{} },
		},
		{
			name: "RegisterGlobalAgentResponse",
			msg:  &goblinv1.RegisterGlobalAgentResponse{SpecUuid: []byte("0123456789abcdef"), Name: "web"},
			fill: func() proto.Message { return &goblinv1.RegisterGlobalAgentResponse{} },
		},
		{
			name: "DeleteGlobalAgentRequest",
			msg:  &goblinv1.DeleteGlobalAgentRequest{AgentId: "web"},
			fill: func() proto.Message { return &goblinv1.DeleteGlobalAgentRequest{} },
		},
		{
			name: "DeleteGlobalAgentResponse",
			msg:  &goblinv1.DeleteGlobalAgentResponse{SpecUuid: []byte("0123456789abcdef"), Name: "web"},
			fill: func() proto.Message { return &goblinv1.DeleteGlobalAgentResponse{} },
		},
		{
			name: "Job",
			msg: &goblinv1.Job{
				Id:            "job-1",
				AgentId:       "agent-1",
				AgentType:     "sleeper",
				AssignedNode:  "node-1",
				Resources:     &goblinv1.ResourceReq{Cpu: 0.5, Memory: 1 << 20},
				Constraints:   map[string]string{"zone": "us-east"},
				Requirements:  map[string]string{"gpu": "true"},
				RestartPolicy: "on-failure",
				Env:           map[string]string{"FOO": "bar"},
			},
			fill: func() proto.Message { return &goblinv1.Job{} },
		},
		{
			name: "SubmitJobRequest",
			msg: &goblinv1.SubmitJobRequest{
				Job: &goblinv1.Job{Id: "job-1", AgentType: "sleeper"},
			},
			fill: func() proto.Message { return &goblinv1.SubmitJobRequest{} },
		},
		{
			name: "SubmitJobResponse",
			msg:  &goblinv1.SubmitJobResponse{JobId: "job-1", AssignedNode: "node-1"},
			fill: func() proto.Message { return &goblinv1.SubmitJobResponse{} },
		},
		{
			name: "DrainNodeRequest",
			msg:  &goblinv1.DrainNodeRequest{NodeId: "node-1"},
			fill: func() proto.Message { return &goblinv1.DrainNodeRequest{} },
		},
		{
			name: "DrainNodeResponse",
			msg:  &goblinv1.DrainNodeResponse{MigratedJobIds: []string{"job-1", "job-2"}},
			fill: func() proto.Message { return &goblinv1.DrainNodeResponse{} },
		},
		{
			name: "PublishEventRequest",
			msg:  &goblinv1.PublishEventRequest{Topic: "deploys", Payload: []byte("v1.2.3")},
			fill: func() proto.Message { return &goblinv1.PublishEventRequest{} },
		},
		{
			name: "PublishEventResponse",
			msg:  &goblinv1.PublishEventResponse{Topic: "deploys"},
			fill: func() proto.Message { return &goblinv1.PublishEventResponse{} },
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
