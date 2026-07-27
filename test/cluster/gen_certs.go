package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// nodeIDPattern confines generated cert filenames: ids become path
// segments under certDir, so anything outside [A-Za-z0-9-] is rejected.
var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

const certDir = "/tmp/goblin-test-certs"

func main() {
	if err := os.RemoveAll(certDir); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(certDir, 0700); err != nil {
		panic(err)
	}

	fmt.Println("[CERTS] Generating CA...")
	caCert, caKey, err := generateCA()
	if err != nil {
		panic(err)
	}

	for _, node := range []string{"node-1", "node-2", "node-3"} {
		fmt.Printf("[CERTS] Generating cert for %s...\n", node)
		if err := generateNodeCert(node, caCert, caKey); err != nil {
			panic(err)
		}
	}

	fmt.Printf("[CERTS] Done! Certificates in %s\n", certDir)
}

func generateCA() (*x509.Certificate, *rsa.PrivateKey, error) {
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(2023),
		Subject:               pkix.Name{CommonName: "Goblin Test CA"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	caBytes, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	if err := writePem(filepath.Join(certDir, "ca.crt"), "CERTIFICATE", caBytes); err != nil {
		return nil, nil, err
	}
	if err := writeKey(filepath.Join(certDir, "ca.key"), caKey); err != nil {
		return nil, nil, err
	}

	return ca, caKey, nil
}

func generateNodeCert(id string, ca *x509.Certificate, caKey *rsa.PrivateKey) error {
	if !nodeIDPattern.MatchString(id) {
		return fmt.Errorf("invalid node id %q: must match %s", id, nodeIDPattern)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: id},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{id, "localhost"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		SubjectKeyId: []byte{1, 2, 3, 4, 6},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}

	if err := writePem(filepath.Join(certDir, id+".crt"), "CERTIFICATE", certBytes); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(certDir, id+".key"), key); err != nil {
		return err
	}

	return nil
}

func writePem(path, typeStr string, bytes []byte) (err error) {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return pem.Encode(out, &pem.Block{Type: typeStr, Bytes: bytes})
}

func writeKey(path string, key *rsa.PrivateKey) (err error) {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return pem.Encode(out, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
