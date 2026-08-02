package supervisor

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"log/slog"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"

	"github.com/goppydae/goblin/internal/logattr"
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
	if err := s.requireOperatorRegistry(fmt.Sprintf("signal %d", signum)); err != nil {
		return err
	}
	if s.issuer == nil || s.revocations == nil {
		return fmt.Errorf("capability issuer not initialized on this node")
	}

	right, err := gapicrypto.RightForSignal(signum)
	if err != nil {
		return err
	}
	tok, _, err := s.issuer.Issue(instUUID, right, 0)
	if err != nil {
		return fmt.Errorf("issue capability token: %w", err)
	}
	// Revocation is deliberately NOT consulted here. This token was
	// minted on the line above, so its UUIDv7 cannot be in the
	// filter; a check would be inert by construction and would
	// suggest to a reader that revocation is enforced on this path.
	// It is enforced where a token actually crosses a trust
	// boundary - checkpointAuthorizer, which receives one from
	// another node (GOBLIN-DIV-015).
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

	// The gossiped locator is consulted here as a DIAGNOSTIC, not as a
	// routing decision. Raft is authoritative for placement and stays
	// so; the locator is the observed runtime identity, and the two
	// disagreeing means one of them is stale - which is exactly the
	// question an operator asks when a signal goes nowhere.
	//
	// It deliberately does NOT enforce the incarnation. That guard
	// already exists and is better placed: procsig.Signal rechecks the
	// start epoch while holding a pidfd pin, on the owning node, against
	// its own tracker - authoritative local state rather than
	// eventually-consistent gossip (GOBLIN-DIV-015).
	if loc, ok := s.scheduler.LookupLocator(instanceID); ok && loc.NodeID != "" && loc.NodeID != nodeID {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
			"placement and observed locator disagree; signalling the placement node",
			logattr.AgentID(instanceID),
			slog.String("raft_node", nodeID),
			slog.String("observed_node", loc.NodeID),
			slog.Uint64("observed_start_epoch", loc.StartEpoch))
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
