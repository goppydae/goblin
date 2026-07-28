package capability_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapiv1 "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	"google.golang.org/protobuf/proto"
)

func resolverFor(i *capability.Issuer) gapicrypto.KeyResolver {
	return func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == i.KeyID() {
			return i.PublicKey(), true
		}
		return nil, false
	}
}

func TestIssue_VerifiesThroughKernelCodepath(t *testing.T) {
	iss, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	inst := ident.NewV7()

	tok, tokenID, err := iss.Issue(inst, gapicrypto.RightSignalTerm, 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(tokenID) != 16 {
		t.Fatalf("token id length = %d, want 16 (UUIDv7)", len(tokenID))
	}

	payload, err := gapicrypto.VerifyCapabilityToken(tok, resolverFor(iss), time.Now(), gapicrypto.RightSignalTerm)
	if err != nil {
		t.Fatalf("VerifyCapabilityToken: %v", err)
	}
	if payload.IssuerNodeId != "node-1" {
		t.Errorf("issuer = %q, want node-1", payload.IssuerNodeId)
	}
	if ident.String(payload.SubjectUuid) != ident.String(inst) {
		t.Errorf("subject mismatch")
	}
}

func TestIssue_TTLClampedToPolicyRange(t *testing.T) {
	iss, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	inst := ident.NewV7()

	cases := []struct {
		req  time.Duration
		want time.Duration
	}{
		{0, capability.TTLDefault},
		{10 * time.Second, capability.TTLMin},
		{1 * time.Hour, capability.TTLMax},
		{200 * time.Second, 200 * time.Second},
	}
	for _, c := range cases {
		tok, _, err := iss.Issue(inst, gapicrypto.RightSignalTerm, c.req)
		if err != nil {
			t.Fatalf("Issue(%v): %v", c.req, err)
		}
		var payload gapiv1.CapabilityTokenPayload
		if err := proto.Unmarshal(tok.Payload, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := time.Duration(payload.ExpiresAtMs-payload.IssuedAtMs) * time.Millisecond
		if got != c.want {
			t.Errorf("Issue(%v) TTL = %v, want %v", c.req, got, c.want)
		}
	}
}

func TestRevocations_RevokedTokenIsFlagged(t *testing.T) {
	r := capability.NewRevocations()
	revoked := ident.NewV7()
	kept := ident.NewV7()

	r.Revoke(revoked)
	if !r.IsRevoked(revoked) {
		t.Fatal("revoked token id not flagged")
	}
	if r.IsRevoked(kept) {
		t.Fatal("false positive on a never-revoked id (statistically near-impossible at this fill)")
	}
}

func TestRevocations_GossipMergeUnions(t *testing.T) {
	a := capability.NewRevocations()
	b := capability.NewRevocations()
	idA, idB := ident.NewV7(), ident.NewV7()
	a.Revoke(idA)
	b.Revoke(idB)

	// b ingests a's snapshot: it now flags both.
	if err := b.Ingest(a.Snapshot()); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !b.IsRevoked(idA) || !b.IsRevoked(idB) {
		t.Fatal("merge did not union the filters")
	}

	// Garbage snapshots are rejected as data, not panics.
	if err := b.Ingest([]byte("short")); err == nil {
		t.Fatal("garbage snapshot accepted")
	}
}
