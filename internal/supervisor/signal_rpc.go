package supervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
)

// memberTagLister is the slice of membership the signal path needs;
// *cluster.Membership satisfies it.
type memberTagLister interface {
	Members() []serf.Member
}

// SignalAgentInstance is the leader-side signal path (GOBLIN-DIV-017,
// DDR-10): issue a capability token for exactly the required right,
// verify it through the kernel's single verification codepath (with
// the revocation filter - defense in depth), commit the request
// through Raft where the FSM authorizes it against the rights bitmap
// and answers with the placement node, then dispatch delivery to that
// node's epoch-guarded pidfd path.
func (s *SchedulerRPC) SignalAgentInstance(req *goblinv1.SignalAgentInstanceRequest, resp *goblinv1.SignalAgentInstanceResponse) error {
	instanceID := req.GetInstanceId()
	signum := req.GetSignum()

	instUUID, err := ident.Parse(instanceID)
	if err != nil {
		return fmt.Errorf("instance id must be a UUID: %w", err)
	}
	if s.issuer == nil || s.revocations == nil {
		return fmt.Errorf("capability issuer not initialized on this node")
	}

	right, err := gapicrypto.RightForSignal(signum)
	if err != nil {
		return err
	}
	tok, tokenID, err := s.issuer.Issue(instUUID, right, 0)
	if err != nil {
		return fmt.Errorf("issue capability token: %w", err)
	}
	if s.revocations.IsRevoked(tokenID) {
		return fmt.Errorf("capability token %s is revoked", ident.String(tokenID))
	}
	payload, err := gapicrypto.VerifyCapabilityToken(tok, s.capabilityKeyResolver(), time.Now(), right)
	if err != nil {
		return fmt.Errorf("verify capability token: %w", err)
	}

	nodeID, err := s.scheduler.Store().SignalInstance(context.Background(), &goblinv1.SignalRequest{
		InstanceUuid: instUUID,
		Signum:       signum,
		TokenId:      payload.TokenId,
		Rights:       payload.Rights,
	})
	if err != nil {
		return fmt.Errorf("signal authorization: %w", err)
	}

	if err := s.scheduler.SignalOnNode(context.Background(), nodeID, instanceID, signum); err != nil {
		return fmt.Errorf("signal delivery on %s: %w", nodeID, err)
	}
	resp.Signum = signum
	resp.InstanceId = instanceID
	resp.NodeId = nodeID
	return nil
}

// capabilityKeyResolver resolves key_id -> issuer public key from the
// cluster's serf tags (cap_key = "<key_id>:<base64 pubkey>"). Unknown
// ids fail closed.
func (s *SchedulerRPC) capabilityKeyResolver() gapicrypto.KeyResolver {
	return func(keyID string) (ed25519.PublicKey, bool) {
		if s.members == nil {
			return nil, false
		}
		for _, m := range s.members.Members() {
			tag := m.Tags["cap_key"]
			if tag == "" {
				continue
			}
			id, b64, found := strings.Cut(tag, ":")
			if !found || id != keyID {
				continue
			}
			pub, err := base64.StdEncoding.DecodeString(b64)
			if err != nil || len(pub) != ed25519.PublicKeySize {
				return nil, false
			}
			return pub, true
		}
		return nil, false
	}
}
