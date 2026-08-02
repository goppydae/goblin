package supervisor

import (
	"crypto/ed25519"
	"log/slog"
	"strings"
	"testing"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	"google.golang.org/protobuf/proto"
)

// checkpointFixture mints a migrate token for one instance and returns
// the authorizer under test, the marshalled token, the instance it is
// bound to, and the token id needed to revoke it.
func checkpointFixture(t *testing.T, revs *capability.Revocations) (fn func(token, inst []byte) error, token, inst, tokenID []byte) {
	t.Helper()

	iss, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	resolver := gapicrypto.KeyResolver(func(keyID string) (ed25519.PublicKey, bool) {
		if keyID == iss.KeyID() {
			return iss.PublicKey(), true
		}
		return nil, false
	})

	right, err := capability.RightForVerb(capability.VerbJobMigrate)
	if err != nil {
		t.Fatalf("RightForVerb: %v", err)
	}

	inst = ident.NewV7()
	tok, tokenID, err := iss.Issue(inst, right, 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	raw, err := proto.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}

	return checkpointAuthorizer(resolver, revs, slog.Default()), raw, inst, tokenID
}

// TestCheckpointAuthorizer_RefusesRevokedToken is the regression for the
// half of GOBLIN-DIV-015 that mattered most.
//
// This is the ONLY path where a capability token crosses a trust
// boundary - the token arrives from the destination node, and what it
// unlocks is the entire address space of a running process. signal_rpc
// and authorize both consult the revocation filter, but each mints its
// token one line earlier, so a freshly minted UUIDv7 can never be in the
// filter and those checks are inert by construction. Revocation was
// checked in the two places it could not matter and not in the one place
// it could.
func TestCheckpointAuthorizer_RefusesRevokedToken(t *testing.T) {
	revs := capability.NewRevocations()
	authorize, token, inst, tokenID := checkpointFixture(t, revs)

	// Control first: the same token, same authorizer, not yet revoked.
	// Without it "refused" proves nothing - a fixture that never
	// authorized would pass the revoked case for the wrong reason.
	if err := authorize(token, inst); err != nil {
		t.Fatalf("token should authorize before revocation: %v", err)
	}

	revs.Revoke(tokenID)

	err := authorize(token, inst)
	if err == nil {
		t.Fatal("a revoked token authorized a checkpoint fetch")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error %q does not name revocation", err)
	}
}

// TestCheckpointAuthorizer_NilRevocationsStillAuthorizes keeps the
// wiring honest in the other direction: a node that has not built a
// revocation filter must not fail every migration closed. The check is
// an added refusal, not a new requirement.
func TestCheckpointAuthorizer_NilRevocationsStillAuthorizes(t *testing.T) {
	authorize, token, inst, _ := checkpointFixture(t, nil)

	if err := authorize(token, inst); err != nil {
		t.Errorf("nil revocations turned a valid token into a refusal: %v", err)
	}
}

// TestCheckpointAuthorizer_RevocationPrecedesSubjectBinding pins the
// ORDER. Both checks refuse, so a test that only asserted "error" would
// pass either way; the message is what distinguishes them. A revoked
// token has no business being considered for any subject.
func TestCheckpointAuthorizer_RevocationPrecedesSubjectBinding(t *testing.T) {
	revs := capability.NewRevocations()
	authorize, token, _, tokenID := checkpointFixture(t, revs)
	revs.Revoke(tokenID)

	// Wrong subject AND revoked: both would refuse, so the message says
	// which check fired first.
	err := authorize(token, ident.NewV7())
	if err == nil {
		t.Fatal("revoked token with a mismatched subject authorized")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("subject binding fired before revocation: %q", err)
	}
}
