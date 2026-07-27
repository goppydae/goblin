package crypto

import (
	"crypto/ed25519"
	"fmt"
	"github.com/goppydae/gapi/internal/safeio"
	"strings"
)

// VerifySignedBinary checks a binary against its sidecar provenance files:
// the .b3 BLAKE3 digest must match the binary's current content, and the
// .sig must be a valid hex-encoded Ed25519 signature over the .b3 file's
// bytes, made by the given public key. This is the same convention
// `gapictl agent verify` checks; both consume this package so verification
// has one codepath (ecosystem provenance rule).
func VerifySignedBinary(binPath string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("verify %s: invalid public key size %d", binPath, len(pub))
	}

	hashPath := binPath + ".b3"
	hashData, err := safeio.ReadFile(hashPath)
	if err != nil {
		return fmt.Errorf("verify %s: reading digest: %w", binPath, err)
	}
	actual, err := HashFile(binPath)
	if err != nil {
		return fmt.Errorf("verify %s: hashing binary: %w", binPath, err)
	}
	if actual != strings.TrimSpace(string(hashData)) {
		return fmt.Errorf("verify %s: digest mismatch (binary does not match its .b3)", binPath)
	}

	sigData, err := safeio.ReadFile(binPath + ".sig")
	if err != nil {
		return fmt.Errorf("verify %s: reading signature: %w", binPath, err)
	}
	sigBytes := make([]byte, ed25519.SignatureSize)
	if _, err := fmt.Sscanf(strings.TrimSpace(string(sigData)), "%x", &sigBytes); err != nil {
		return fmt.Errorf("verify %s: decoding signature: %w", binPath, err)
	}
	if !Verify(pub, hashData, sigBytes) {
		return fmt.Errorf("verify %s: signature does not verify against the provided key", binPath)
	}
	return nil
}
