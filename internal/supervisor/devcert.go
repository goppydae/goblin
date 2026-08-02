package supervisor

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"time"
)

// certDNSNames returns the SANs for the dev cert: localhost plus the
// configured node id and the resolved hostname (deduplicated).
func certDNSNames(nodeID string) []string {
	names := []string{"localhost"}
	seen := map[string]bool{"localhost": true}
	for _, n := range []string{nodeID, resolvedHostname()} {
		if n != "" && !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	return names
}

func resolvedHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// generateDocsCert builds the dev-mode self-signed cert from the real
// node identity (nodeID + resolved hostname), not a hardcoded node list
// (R21). Dev-only: production fails closed before reaching this path.
func generateDocsCert(nodeID string) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Valid for 1 year
	notBefore := time.Now().Add(-1 * time.Minute)
	notAfter := time.Now().Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")},
		DNSNames:     certDNSNames(nodeID),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
