package capability

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// Operator identity (GOBLIN-DIV-015 piece 1). An operator is an Ed25519
// keypair, independent of TLS: the default deployment does not require
// mTLS, so the transport cannot be the thing that says who is calling.
//
// Nothing here touches Raft or the clock. The key-id, signing, and
// verification helpers are pure functions of their arguments, because
// the FSM calls them inside Apply and Apply must decide identically on
// every replica.
//
// LoadOperatorKey is the exception and must NEVER be called from Apply:
// it reads local disk, and replicas do not have the same files. It runs
// at node startup, to build a seed the node then proposes through Raft.
var (
	// ErrOperatorKeyMalformed covers a wrong-sized key, a key id that
	// does not match its bytes, and an unparseable key file.
	ErrOperatorKeyMalformed = errors.New("operator key: malformed")
	// ErrOperatorKeyUnknown means the authorizing key id resolved to
	// nothing. Fail closed: an unresolvable signer is not a signer.
	ErrOperatorKeyUnknown = errors.New("operator key: unknown key id")
	// ErrOperatorKeySignature means the Ed25519 check failed.
	ErrOperatorKeySignature = errors.New("operator key change: signature verification failed")
)

// OperatorKeyID derives a key's registry id: lowercase hex SHA-256 over
// the raw 32 public key bytes.
//
// Deliberately not gapicrypto.PEM.FingerprintPublicKey, which hashes
// the PKIX DER encoding. The registry stores raw key bytes, so the id
// hashes raw key bytes; going through DER would make the id depend on
// an encoding the registry never sees.
func OperatorKeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// NewOperatorKey builds a registry record with its id derived.
func NewOperatorKey(pub ed25519.PublicKey, comment string) (*goblinv1.OperatorKey, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key size %d, want %d",
			ErrOperatorKeyMalformed, len(pub), ed25519.PublicKeySize)
	}
	return &goblinv1.OperatorKey{
		KeyId:     OperatorKeyID(pub),
		PublicKey: append([]byte(nil), pub...),
		Comment:   comment,
	}, nil
}

// ValidateOperatorKey checks a record's internal consistency. The FSM
// calls it on every record it is asked to store, so a record whose id
// lies about its bytes never reaches replicated state.
func ValidateOperatorKey(k *goblinv1.OperatorKey) error {
	if k == nil {
		return fmt.Errorf("%w: nil record", ErrOperatorKeyMalformed)
	}
	if len(k.GetPublicKey()) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key size %d, want %d",
			ErrOperatorKeyMalformed, len(k.GetPublicKey()), ed25519.PublicKeySize)
	}
	want := OperatorKeyID(k.GetPublicKey())
	if k.GetKeyId() != want {
		return fmt.Errorf("%w: key id %q does not match its public key (want %q)",
			ErrOperatorKeyMalformed, k.GetKeyId(), want)
	}
	return nil
}

// LoadOperatorKey reads a hex-encoded Ed25519 public key from disk - the
// same on-disk shape gapi's LoadPublic writes, so an operator key and an
// agent verify key are produced by the same tooling.
func LoadOperatorKey(path string) (*goblinv1.OperatorKey, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("read operator key %s: %w", path, err)
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not hex: %w", ErrOperatorKeyMalformed, path, err)
	}
	// The comment is replicated cluster state and is what an operator
	// will see listed, so it carries the file's base name only: the
	// seeding node's full local path is deliberately not replicated -
	// it describes one machine's disk, not the key. A real
	// operator-supplied label is piece 2's job (GOBLIN-DIV-015).
	k, err := NewOperatorKey(pub, filepath.Base(path))
	if err != nil {
		return nil, fmt.Errorf("operator key %s: %w", path, err)
	}
	return k, nil
}

// SignOperatorKeyChange serializes payload and signs the literal bytes,
// mirroring SignCapabilityToken: protobuf serialization is not
// canonical, so verifiers must never re-serialize before checking.
func SignOperatorKeyChange(payload *goblinv1.OperatorKeyChangePayload, priv ed25519.PrivateKey) (*goblinv1.OperatorKeyChange, error) {
	if payload == nil {
		return nil, fmt.Errorf("%w: nil payload", ErrOperatorKeyMalformed)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing operator key change: private key size %d, want %d",
			len(priv), ed25519.PrivateKeySize)
	}
	raw, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("signing operator key change: marshal payload: %w", err)
	}
	return &goblinv1.OperatorKeyChange{
		Payload:   raw,
		Signature: ed25519.Sign(priv, raw),
	}, nil
}

// OperatorKeyResolver maps an authorizing key id to its public key.
// Returning false fails the change closed with ErrOperatorKeyUnknown.
type OperatorKeyResolver func(keyID string) (ed25519.PublicKey, bool)

// VerifyOperatorKeyChange checks structure and signature and returns the
// verified payload. Like VerifyCapabilityToken it parses before
// verifying only to learn the key id, and trusts no other field until
// the signature has passed.
//
// It checks no expiry, because it runs inside FSM Apply where the clock
// is not a shared input. Replay is the FSM's job, via prev_serial.
func VerifyOperatorKeyChange(chg *goblinv1.OperatorKeyChange, resolve OperatorKeyResolver) (*goblinv1.OperatorKeyChangePayload, error) {
	if chg == nil || len(chg.GetPayload()) == 0 || len(chg.GetSignature()) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: missing payload or signature", ErrOperatorKeyMalformed)
	}
	if resolve == nil {
		return nil, fmt.Errorf("%w: no resolver configured", ErrOperatorKeyUnknown)
	}

	var payload goblinv1.OperatorKeyChangePayload
	if err := proto.Unmarshal(chg.GetPayload(), &payload); err != nil {
		return nil, fmt.Errorf("%w: undecodable payload: %w", ErrOperatorKeyMalformed, err)
	}

	pub, ok := resolve(payload.GetAuthorizingKeyId())
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrOperatorKeyUnknown, payload.GetAuthorizingKeyId())
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: resolved key size %d, want %d",
			ErrOperatorKeyUnknown, len(pub), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(pub, chg.GetPayload(), chg.GetSignature()) {
		return nil, ErrOperatorKeySignature
	}
	return &payload, nil
}
