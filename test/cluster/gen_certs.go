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
	"time"
)

const certDir = "/tmp/goblin-test-certs"

func main() {
	if err := os.RemoveAll(certDir); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(certDir, 0755); err != nil {
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

	writePem(filepath.Join(certDir, "ca.crt"), "CERTIFICATE", caBytes)
	writeKey(filepath.Join(certDir, "ca.key"), caKey)

	return ca, caKey, nil
}

func generateNodeCert(id string, ca *x509.Certificate, caKey *rsa.PrivateKey) error {
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

	writePem(filepath.Join(certDir, id+".crt"), "CERTIFICATE", certBytes)
	writeKey(filepath.Join(certDir, id+".key"), key)

	return nil
}

func writePem(path, typeStr string, bytes []byte) {
	out, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	pem.Encode(out, &pem.Block{Type: typeStr, Bytes: bytes})
}

func writeKey(path string, key *rsa.PrivateKey) {
	out, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer out.Close()
	pem.Encode(out, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}
