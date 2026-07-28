package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapiv1 "github.com/goppydae/gapi/pkg/proto"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
)

// authorize gates one mutating verb behind a capability token, the same
// way SignalAgentInstance gates signal delivery (GOBLIN-DIV-015,
// DDR-9): mint a token carrying exactly the verb's right and scoped to
// exactly the resource, check it against the gossip-merged revocation
// filter, then verify it through the kernel's single verification
// codepath.
//
// The token is self-issued today - the leader mints it for its own
// request - so this does not yet authenticate a remote caller. What it
// does buy is real and is why it goes in now: every mutating verb
// passes through one authorization codepath that refuses ungrantable
// verbs, binds the action to a named subject, and stamps an auditable,
// revocable token id on it. Caller-supplied tokens plug into this seam
// without moving any call site.
//
// subject is the resource's stable UUIDv5 (specSubject, nodeSubject,
// ...), so a token minted for one resource cannot authorize a verb
// against another.
func (s *SchedulerRPC) authorize(verb capability.Verb, subject []byte) (*gapiv1.CapabilityTokenPayload, error) {
	if s.issuer == nil || s.revocations == nil {
		return nil, fmt.Errorf("capability issuer not initialized on this node")
	}

	right, err := capability.RightForVerb(verb)
	if err != nil {
		return nil, err
	}

	tok, tokenID, err := s.issuer.Issue(subject, right, 0)
	if err != nil {
		return nil, fmt.Errorf("issue capability token for %s: %w", verb, err)
	}
	if s.revocations.IsRevoked(tokenID) {
		return nil, fmt.Errorf("capability token %s is revoked", ident.String(tokenID))
	}

	payload, err := gapicrypto.VerifyCapabilityToken(tok, s.capabilityKeyResolver(), time.Now(), right)
	if err != nil {
		return nil, fmt.Errorf("verify capability token for %s: %w", verb, err)
	}

	// The audit line is the point of self-issuance: every mutation
	// carries a token id an operator can trace and revoke.
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "verb authorized",
		slog.String("verb", string(verb)),
		slog.String("subject", ident.String(subject)),
		slog.String("token_id", ident.String(payload.TokenId)))
	return payload, nil
}

// Subject derivations. Every mutating verb names its resource; keeping
// the naming scheme in one place is what lets a token be scoped without
// a store lookup (the UUIDs are pure functions of the names).
func specSubject(name string) []byte   { return ident.SpecUUID(name) }
func nodeSubject(nodeID string) []byte { return ident.NodeUUID(nodeID) }
func jobSubject(jobID string) []byte   { return ident.NewV5("job/" + jobID) }
func topicSubject(topic string) []byte { return ident.NewV5("topic/" + topic) }
