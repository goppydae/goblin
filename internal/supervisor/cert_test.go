package supervisor

import (
	"crypto/x509"
	"os"
	"testing"
)

// TestGenerateDocsCert_RealIdentitySANs verifies R21: the dev cert's
// SANs come from the configured node id and resolved hostname, not a
// hardcoded node-1..node-5 list.
func TestGenerateDocsCert_RealIdentitySANs(t *testing.T) {
	cert, err := generateDocsCert("my-node-42")
	if err != nil {
		t.Fatalf("generateDocsCert: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}

	sans := map[string]bool{}
	for _, n := range leaf.DNSNames {
		sans[n] = true
	}

	if !sans["localhost"] || !sans["my-node-42"] {
		t.Errorf("SANs missing required names, got %v", leaf.DNSNames)
	}
	if host, err := os.Hostname(); err == nil && host != "" && host != "my-node-42" && !sans[host] {
		t.Errorf("SANs missing resolved hostname %q, got %v", host, leaf.DNSNames)
	}
	for _, stale := range []string{"node-1", "node-5", "controller"} {
		if sans[stale] {
			t.Errorf("SANs still carry hardcoded name %q", stale)
		}
	}
}
