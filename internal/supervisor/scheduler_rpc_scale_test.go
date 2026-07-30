package supervisor

import (
	"testing"

	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

// TestScaleAgentMessages_RoundTrip pins the wire encoding: these
// messages exist so buf breaking can see the fields, which is only true
// while they marshal and unmarshal as protobuf.
func TestScaleAgentMessages_RoundTrip(t *testing.T) {
	req := &goblinv1.ScaleAgentRequest{AgentId: "web", Replicas: 5}

	raw, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var got goblinv1.ScaleAgentRequest
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if got.GetAgentId() != "web" || got.GetReplicas() != 5 {
		t.Errorf("round trip = (%q, %d), want (web, 5)", got.GetAgentId(), got.GetReplicas())
	}

	resp := &goblinv1.ScaleAgentResponse{SpecUuid: []byte("0123456789abcdef"), Replicas: 5}
	rawResp, err := proto.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var gotResp goblinv1.ScaleAgentResponse
	if err := proto.Unmarshal(rawResp, &gotResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if string(gotResp.GetSpecUuid()) != "0123456789abcdef" {
		t.Errorf("spec_uuid = %q, want the 16-byte input", gotResp.GetSpecUuid())
	}
}
