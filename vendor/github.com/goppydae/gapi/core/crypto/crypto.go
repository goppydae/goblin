package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

// PEM block types for Ed25519 private keys.
//
// pemTypePKCS8 is the canonical format: a PKCS#8 DER key in a "PRIVATE KEY"
// block, matching PEMNS.EncodePrivateKey/ParsePrivateKey so keys are
// interchangeable across both persistence paths. pemTypeLegacyEd25519 is the
// old raw-key format still accepted by LoadPrivate for backward compatibility.
const (
	pemTypePKCS8         = "PRIVATE KEY"
	pemTypeLegacyEd25519 = "ED25519 PRIVATE KEY"
)

// KeyPair holds the private and public keys
type KeyPair struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateKey creates a new Ed25519 keypair
func GenerateKey() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &KeyPair{Public: pub, Private: priv}, nil
}

// Sign signs data with the private key
func (k *KeyPair) Sign(data []byte) []byte {
	return ed25519.Sign(k.Private, data)
}

// Verify verifies the signature against the public key
func Verify(pub ed25519.PublicKey, data, sig []byte) bool {
	return ed25519.Verify(pub, data, sig)
}

// HashFile returns the BLAKE3 hash of a file as a hex string
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}

	h := blake3.New()
	_, err = io.Copy(h, f)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// SavePrivate saves the private key to a PEM file in PKCS#8 form ("PRIVATE KEY"
// block), the same format PEMNS.EncodePrivateKey produces, so a key written here
// can be loaded through either code path.
func (k *KeyPair) SavePrivate(path string) error {
	der, err := x509.MarshalPKCS8PrivateKey(k.Private)
	if err != nil {
		return fmt.Errorf("marshal pkcs8 private key: %w", err)
	}
	block := &pem.Block{
		Type:  pemTypePKCS8,
		Bytes: der,
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = pem.Encode(f, block)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// LoadPrivate loads a private key from a PEM file. It accepts the canonical
// PKCS#8 "PRIVATE KEY" format as well as the legacy raw "ED25519 PRIVATE KEY"
// format for backward compatibility (legacy keys are upgraded to PKCS#8 the next
// time SavePrivate runs).
func LoadPrivate(path string) (*KeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("invalid pem data")
	}

	var priv ed25519.PrivateKey
	switch block.Type {
	case pemTypePKCS8:
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse pkcs8 private key: %w", err)
		}
		ed, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("unexpected private key type %T, want ed25519", key)
		}
		priv = ed
	case pemTypeLegacyEd25519:
		if len(block.Bytes) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("invalid ed25519 private key length %d, want %d", len(block.Bytes), ed25519.PrivateKeySize)
		}
		priv = ed25519.PrivateKey(append([]byte(nil), block.Bytes...))
	default:
		return nil, fmt.Errorf("unsupported private key PEM type %q", block.Type)
	}

	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to derive ed25519 public key")
	}
	return &KeyPair{Private: priv, Public: pub}, nil
}

// SavePublic saves the public key to a hex file (simple format for now)
func (k *KeyPair) SavePublic(path string) error {
	return os.WriteFile(path, []byte(hex.EncodeToString(k.Public)), 0644)
}

// LoadPublic loads a public key from a hex file
func LoadPublic(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(string(data))
}
