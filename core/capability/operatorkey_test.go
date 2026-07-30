package capability

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	goblinv1 "github.com/goppydae/goblin/proto"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func TestOperatorKeyIDIsHexSHA256OfRawKey(t *testing.T) {
	pub, _ := testKey(t)
	sum := sha256.Sum256(pub)
	if got, want := OperatorKeyID(pub), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("OperatorKeyID = %q, want %q", got, want)
	}
}

func TestValidateOperatorKeyRejectsMismatchedID(t *testing.T) {
	pub, _ := testKey(t)
	k, err := NewOperatorKey(pub, "ops")
	if err != nil {
		t.Fatalf("NewOperatorKey: %v", err)
	}
	k.KeyId = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := ValidateOperatorKey(k); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("ValidateOperatorKey with a lying key id = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestValidateOperatorKeyRejectsWrongKeySize(t *testing.T) {
	k := &goblinv1.OperatorKey{KeyId: "abc", PublicKey: []byte{1, 2, 3}}
	if err := ValidateOperatorKey(k); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("ValidateOperatorKey with a 3-byte key = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestLoadOperatorKeyReadsHexFile(t *testing.T) {
	pub, _ := testKey(t)
	path := filepath.Join(t.TempDir(), "op.pub")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(pub)), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	k, err := LoadOperatorKey(path)
	if err != nil {
		t.Fatalf("LoadOperatorKey: %v", err)
	}
	if k.GetKeyId() != OperatorKeyID(pub) {
		t.Fatalf("key id = %q, want %q", k.GetKeyId(), OperatorKeyID(pub))
	}
	if string(k.GetPublicKey()) != string(pub) {
		t.Fatalf("public key bytes differ from the file contents")
	}
}

func TestLoadOperatorKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op.pub")
	if err := os.WriteFile(path, []byte("not hex"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := LoadOperatorKey(path); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("LoadOperatorKey on garbage = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestSignAndVerifyOperatorKeyChangeRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	added, _ := testKey(t)
	addRec, err := NewOperatorKey(added, "second operator")
	if err != nil {
		t.Fatalf("NewOperatorKey: %v", err)
	}
	payload := &goblinv1.OperatorKeyChangePayload{
		Op:               goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD,
		Key:              addRec,
		PrevSerial:       7,
		AuthorizingKeyId: OperatorKeyID(pub),
	}
	chg, err := SignOperatorKeyChange(payload, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}

	resolve := func(id string) (ed25519.PublicKey, bool) {
		if id == OperatorKeyID(pub) {
			return pub, true
		}
		return nil, false
	}
	got, err := VerifyOperatorKeyChange(chg, resolve)
	if err != nil {
		t.Fatalf("VerifyOperatorKeyChange: %v", err)
	}
	if got.GetPrevSerial() != 7 || got.GetKey().GetKeyId() != addRec.GetKeyId() {
		t.Fatalf("verified payload does not match what was signed: %+v", got)
	}
}

// TestVerifyOperatorKeyChangeRejectsSubstitutedPayload swaps in a
// DIFFERENT well-formed payload under the original signature. Tampering
// by flipping bytes is not a usable test here: the payload's last field
// is a proto3 string, so a flipped byte usually breaks UTF-8 and the
// unmarshal fails first, which is a malformed error and not the
// signature check this test exists to exercise.
func TestVerifyOperatorKeyChangeRejectsSubstitutedPayload(t *testing.T) {
	pub, priv := testKey(t)
	signed := &goblinv1.OperatorKeyChangePayload{
		Op:               goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		Key:              &goblinv1.OperatorKey{KeyId: "deadbeef"},
		PrevSerial:       1,
		AuthorizingKeyId: OperatorKeyID(pub),
	}
	chg, err := SignOperatorKeyChange(signed, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}

	// Same shape, different serial. Decodes cleanly; the signature was
	// never over these bytes.
	substituted, err := proto.Marshal(&goblinv1.OperatorKeyChangePayload{
		Op:               goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_REMOVE,
		Key:              &goblinv1.OperatorKey{KeyId: "deadbeef"},
		PrevSerial:       2,
		AuthorizingKeyId: OperatorKeyID(pub),
	})
	if err != nil {
		t.Fatalf("marshal substituted payload: %v", err)
	}
	chg.Payload = substituted

	resolve := func(string) (ed25519.PublicKey, bool) { return pub, true }
	if _, err := VerifyOperatorKeyChange(chg, resolve); !errors.Is(err, ErrOperatorKeySignature) {
		t.Fatalf("VerifyOperatorKeyChange on a substituted payload = %v, want ErrOperatorKeySignature", err)
	}
}

// TestVerifyOperatorKeyChangeRejectsUndecodablePayload pins the ORDER,
// not just the error: bytes that are not a payload are refused before
// any key lookup, so a garbage entry in the Raft log cannot reach the
// resolver. Asserting the error alone would not catch a reordering -
// this file already shipped, and reverted, a version that checked the
// signature first.
func TestVerifyOperatorKeyChangeRejectsUndecodablePayload(t *testing.T) {
	pub, priv := testKey(t)
	garbage := []byte{0xff, 0xff, 0xff, 0xff}
	chg := &goblinv1.OperatorKeyChange{
		Payload: garbage,
		// A genuine signature over the garbage. If the implementation
		// ever verifies before decoding, this would pass the signature
		// check - which is exactly why the resolver assertion below,
		// and not the error value, is what makes this test bite.
		Signature: ed25519.Sign(priv, garbage),
	}

	resolverCalled := false
	resolve := func(string) (ed25519.PublicKey, bool) {
		resolverCalled = true
		return pub, true
	}
	if _, err := VerifyOperatorKeyChange(chg, resolve); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("VerifyOperatorKeyChange on undecodable bytes = %v, want ErrOperatorKeyMalformed", err)
	}
	if resolverCalled {
		t.Fatal("undecodable payload reached the key resolver; the unmarshal must be refused first")
	}
}

func TestVerifyOperatorKeyChangeRejectsUnknownSigner(t *testing.T) {
	pub, priv := testKey(t)
	payload := &goblinv1.OperatorKeyChangePayload{
		Op:               goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD,
		Key:              &goblinv1.OperatorKey{KeyId: "x"},
		AuthorizingKeyId: OperatorKeyID(pub),
	}
	chg, err := SignOperatorKeyChange(payload, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}
	resolve := func(string) (ed25519.PublicKey, bool) { return nil, false }
	if _, err := VerifyOperatorKeyChange(chg, resolve); !errors.Is(err, ErrOperatorKeyUnknown) {
		t.Fatalf("VerifyOperatorKeyChange with no resolvable signer = %v, want ErrOperatorKeyUnknown", err)
	}
}

func TestNewOperatorKeyRejectsWrongSizeKey(t *testing.T) {
	if _, err := NewOperatorKey([]byte{1, 2, 3}, "short"); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("NewOperatorKey with a 3-byte key = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestValidateOperatorKeyRejectsNil(t *testing.T) {
	if err := ValidateOperatorKey(nil); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("ValidateOperatorKey(nil) = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestLoadOperatorKeyRejectsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.pub")
	if _, err := LoadOperatorKey(path); err == nil {
		t.Fatal("LoadOperatorKey on a missing file returned no error")
	}
}

func TestLoadOperatorKeyRejectsDirectory(t *testing.T) {
	if _, err := LoadOperatorKey(t.TempDir()); err == nil {
		t.Fatal("LoadOperatorKey on a directory returned no error")
	}
}

func TestLoadOperatorKeyRejectsWrongLengthHex(t *testing.T) {
	// Clean hex, but 16 bytes rather than 32. Decoding succeeds and the
	// size check is the only thing standing between this and a registry
	// entry nobody holds the private half of.
	path := filepath.Join(t.TempDir(), "short.pub")
	if err := os.WriteFile(path, []byte("00112233445566778899aabbccddeeff"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	if _, err := LoadOperatorKey(path); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("LoadOperatorKey on 16 bytes of hex = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestLoadOperatorKeyToleratesTrailingWhitespace(t *testing.T) {
	pub, _ := testKey(t)
	path := filepath.Join(t.TempDir(), "op.pub")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(pub)+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	k, err := LoadOperatorKey(path)
	if err != nil {
		t.Fatalf("LoadOperatorKey with a trailing newline: %v", err)
	}
	if k.GetKeyId() != OperatorKeyID(pub) {
		t.Fatalf("key id = %q, want %q", k.GetKeyId(), OperatorKeyID(pub))
	}
}

func TestSignOperatorKeyChangeRejectsNilPayload(t *testing.T) {
	_, priv := testKey(t)
	if _, err := SignOperatorKeyChange(nil, priv); !errors.Is(err, ErrOperatorKeyMalformed) {
		t.Fatalf("SignOperatorKeyChange(nil) = %v, want ErrOperatorKeyMalformed", err)
	}
}

func TestSignOperatorKeyChangeRejectsWrongSizePrivateKey(t *testing.T) {
	payload := &goblinv1.OperatorKeyChangePayload{Op: goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD}
	if _, err := SignOperatorKeyChange(payload, []byte{1, 2, 3}); err == nil {
		t.Fatal("SignOperatorKeyChange with a 3-byte private key returned no error")
	}
}

func TestVerifyOperatorKeyChangeRejectsMalformedEnvelope(t *testing.T) {
	pub, _ := testKey(t)
	resolve := func(string) (ed25519.PublicKey, bool) { return pub, true }

	cases := map[string]*goblinv1.OperatorKeyChange{
		"nil change":      nil,
		"empty payload":   {Payload: nil, Signature: make([]byte, ed25519.SignatureSize)},
		"short signature": {Payload: []byte{1}, Signature: []byte{1, 2, 3}},
		"no signature":    {Payload: []byte{1}, Signature: nil},
	}
	for name, chg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyOperatorKeyChange(chg, resolve); !errors.Is(err, ErrOperatorKeyMalformed) {
				t.Fatalf("VerifyOperatorKeyChange(%s) = %v, want ErrOperatorKeyMalformed", name, err)
			}
		})
	}
}

func TestVerifyOperatorKeyChangeRejectsNilResolver(t *testing.T) {
	_, priv := testKey(t)
	chg, err := SignOperatorKeyChange(&goblinv1.OperatorKeyChangePayload{
		Op: goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD,
	}, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}
	if _, err := VerifyOperatorKeyChange(chg, nil); !errors.Is(err, ErrOperatorKeyUnknown) {
		t.Fatalf("VerifyOperatorKeyChange with a nil resolver = %v, want ErrOperatorKeyUnknown", err)
	}
}

func TestVerifyOperatorKeyChangeRejectsWrongSizeResolvedKey(t *testing.T) {
	_, priv := testKey(t)
	chg, err := SignOperatorKeyChange(&goblinv1.OperatorKeyChangePayload{
		Op: goblinv1.OperatorKeyOp_OPERATOR_KEY_OP_ADD,
	}, priv)
	if err != nil {
		t.Fatalf("SignOperatorKeyChange: %v", err)
	}
	// A registry that somehow holds a truncated key must fail closed
	// rather than hand short bytes to ed25519.Verify.
	resolve := func(string) (ed25519.PublicKey, bool) { return []byte{1, 2, 3}, true }
	if _, err := VerifyOperatorKeyChange(chg, resolve); !errors.Is(err, ErrOperatorKeyUnknown) {
		t.Fatalf("VerifyOperatorKeyChange with a 3-byte resolved key = %v, want ErrOperatorKeyUnknown", err)
	}
}
