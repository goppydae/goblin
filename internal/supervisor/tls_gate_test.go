package supervisor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRun_ProductionModeRequiresTLS verifies the fail-closed TLS posture:
// production mode with no cert/key material must refuse to start instead
// of silently falling back to an unverified ephemeral certificate.
func TestRun_ProductionModeRequiresTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sup := New(Config{
		NodeID:         "tls-gate-test",
		ProductionMode: true,
	})

	err := sup.Run(ctx)
	if err == nil {
		t.Fatal("Run() in production mode without TLS should fail closed")
	}
	if !strings.Contains(err.Error(), "production mode requires TLS") {
		t.Errorf("Run() error = %v, want the production TLS gate error", err)
	}
}

// TestRun_ProductionModeRequiresMTLS is GOBLIN-DIV-043's boot half.
//
// TLS alone leaves ClientAuth at NoClientCert, so the shared listener
// admits any peer that completes a handshake - and on the raft plane
// that peer can send InstallSnapshot, which FSM.Restore applies without
// passing through Apply and its registry guards. mTLS being available
// was never the same as mTLS being required; the gap was a warning that
// production could run straight past.
//
// The cert and key are deliberately real files here. Pointing at
// nonexistent paths would fail earlier, in cert loading, and the test
// would pass without ever reaching the branch it exists to pin.
func TestRun_ProductionModeRequiresMTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dir := t.TempDir()
	certPath, keyPath := writeSelfSignedPair(t, dir)

	sup := New(Config{
		NodeID:         "mtls-gate-test",
		ProductionMode: true,
		CertFile:       certPath,
		KeyFile:        keyPath,
		// CAFile deliberately empty: this is the configuration that
		// previously logged a warning and carried on.
	})

	err := sup.Run(ctx)
	if err == nil {
		t.Fatal("Run() in production mode without a CA should fail closed; " +
			"an unauthenticated peer could reach the raft plane")
	}
	if !strings.Contains(err.Error(), "production mode requires mTLS") {
		t.Errorf("Run() error = %v, want the production mTLS gate error", err)
	}
}

// writeSelfSignedPair emits a throwaway cert/key pair on disk so the
// TLS branch under test is reached with loadable material.
func writeSelfSignedPair(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mtls-gate-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certPath = filepath.Join(dir, "node.crt")
	keyPath = filepath.Join(dir, "node.key")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
	return certPath, keyPath
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		_ = f.Close()
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
