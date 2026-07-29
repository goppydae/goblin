package crypto

import (
	"crypto/ed25519"
	"fmt"
	"strings"

	"github.com/goppydae/gapi/internal/safeio"
)

// VerifySignedBinary checks a binary against its sidecar provenance files:
// the .b3 BLAKE3 digest must match the binary's current content, and the
// .sig must be a valid hex-encoded Ed25519 signature over that digest,
// made by the given public key. This is the same convention
// `gapictl agent verify` checks; both consume this package so verification
// has one codepath (ecosystem provenance rule).
//
// The signature covers the CANONICAL digest string - exactly what
// HashFile returns - and not the raw bytes of the .b3 file. Those two
// differ: Magefile writes the sidecar as hexSum+"\n", so signing the
// file bytes would bind the signature to incidental formatting, and a
// .b3 rewritten without the trailing newline would stop verifying
// against an unchanged binary. Signing the digest keeps the chain
// pubkey -> digest -> binary independent of how the sidecar is spelled.
//
// This asymmetry was a live defect (GAPI-DIV-032): the digest
// comparison trimmed and the signature check did not, so no signature
// produced by 'gapictl crypto sign' could ever verify, and production
// mode - which gates agent start on this - could not start any agent.
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
	// Verify over the canonical digest, which is what the signer covers.
	// Passing hashData here would carry the sidecar's trailing newline
	// into the signed message and never verify.
	if !Verify(pub, []byte(actual), sigBytes) {
		return fmt.Errorf("verify %s: signature does not verify against the provided key", binPath)
	}
	return nil
}
