package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/blake3"
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
	defer f.Close()

	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// SavePrivate saves the private key to a PEM file
func (k *KeyPair) SavePrivate(path string) error {
	block := &pem.Block{
		Type:  "ED25519 PRIVATE KEY",
		Bytes: k.Private,
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, block)
}

// LoadPrivate loads a private key from a PEM file
func LoadPrivate(path string) (*KeyPair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil || block.Type != "ED25519 PRIVATE KEY" {
		return nil, fmt.Errorf("invalid pem data")
	}

	priv := ed25519.PrivateKey(block.Bytes)
	pub := priv.Public().(ed25519.PublicKey)
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
