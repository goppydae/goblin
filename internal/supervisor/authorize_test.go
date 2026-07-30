package supervisor

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/hashicorp/serf/serf"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// staticMembers publishes one member carrying the issuer's cap_key tag,
// which is how the verifier resolves key_id -> public key in a real
// cluster (gossip). No network, no serf agent.
type staticMembers []serf.Member

func (m staticMembers) Members() []serf.Member { return m }

func membersFor(i *capability.Issuer) staticMembers {
	return staticMembers{{
		Name: "node-1",
		Tags: map[string]string{
			"cap_key": i.KeyID() + ":" + base64.StdEncoding.EncodeToString(i.PublicKey()),
		},
	}}
}

// A SchedulerRPC with no issuer and no scheduler is the sharpest probe
// available: if a verb's capability gate is missing, or runs after the
// action instead of before it, the call reaches a nil scheduler and
// panics rather than returning the refusal.
func mutatingVerbs() map[string]func(*SchedulerRPC) error {
	node := "node-1"
	agent := "web"
	return map[string]func(*SchedulerRPC) error{
		"DrainNode": func(s *SchedulerRPC) error {
			var out []string
			return s.DrainNode(&node, &out)
		},
		"MigrateJob": func(s *SchedulerRPC) error {
			var out string
			return s.MigrateJob(&MigrateRequest{JobID: "j1", ToNode: "node-2"}, &out)
		},
		"ScaleAgent": func(s *SchedulerRPC) error {
			var out goblinv1.ScaleAgentResponse
			return s.ScaleAgent(&goblinv1.ScaleAgentRequest{AgentId: agent, Replicas: 3}, &out)
		},
		"DeleteGlobalAgent": func(s *SchedulerRPC) error {
			var out string
			return s.DeleteGlobalAgent(&agent, &out)
		},
		"PublishEvent": func(s *SchedulerRPC) error {
			var out string
			return s.PublishEvent(&PublishRequest{Topic: "t", Payload: []byte("x")}, &out)
		},
	}
}

func TestMutatingVerbs_RefuseWithoutACapabilityIssuer(t *testing.T) {
	for name, call := range mutatingVerbs() {
		t.Run(name, func(t *testing.T) {
			err := call(&SchedulerRPC{})
			if err == nil {
				t.Fatal("verb ran without a capability issuer")
			}
			if !strings.Contains(err.Error(), "capability issuer not initialized") {
				t.Errorf("err = %v, want the capability refusal", err)
			}
		})
	}
}

func TestAuthorize_UngrantableVerbFailsClosed(t *testing.T) {
	issuer, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("new issuer: %v", err)
	}
	s := &SchedulerRPC{issuer: issuer, revocations: capability.NewRevocations()}

	if _, err := s.authorize("agent.exfiltrate", specSubject("web")); !errors.Is(err, gapicrypto.ErrTokenRights) {
		t.Fatalf("err = %v, want ErrTokenRights", err)
	}
}

func TestAuthorize_ScopesTheTokenToTheNamedSubject(t *testing.T) {
	issuer, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("new issuer: %v", err)
	}
	s := &SchedulerRPC{
		issuer:      issuer,
		revocations: capability.NewRevocations(),
		members:     membersFor(issuer),
	}

	payload, err := s.authorize(capability.VerbAgentScale, specSubject("web"))
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if string(payload.SubjectUuid) != string(specSubject("web")) {
		t.Error("token subject is not the named agent")
	}
	if string(payload.SubjectUuid) == string(specSubject("api")) {
		t.Error("token for web would authorize api")
	}
	// Exactly the verb's right, nothing more: a scale token must not
	// carry delete or any signal right.
	if payload.Rights != capability.RightAgentScale {
		t.Errorf("rights = %#x, want exactly RightAgentScale (%#x)", payload.Rights, capability.RightAgentScale)
	}
}
