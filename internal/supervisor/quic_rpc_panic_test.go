package supervisor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	goblintransport "github.com/goppydae/goblin/core/transport"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
)

// panicHarness starts a real QUICRPCServer on loopback and returns a
// connected client. Cert handling follows the pinned-DER pattern from
// core/transport/listener_test.go: the self-signed cert has no SANs, so
// the client skips chain verification and pins the exact certificate.
func panicHarness(t *testing.T) *QUICRPCClient {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "panic-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{goblintransport.ALPNGoblinRPC},
	}
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	ln, err := quic.Listen(udpConn, serverTLS, nil)
	if err != nil {
		t.Fatalf("quic listen: %v", err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Logf("close listener: %v", cerr)
		}
	})

	server := NewQUICRPCServer()
	server.RegisterHandler("test.Panic", func(payload []byte) ([]byte, error) {
		panic("deliberate handler panic for GOBLIN-DIV-041")
	})
	server.RegisterHandler("test.OK", func(payload []byte) ([]byte, error) {
		return payload, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			conn, aerr := ln.Accept(ctx)
			if aerr != nil {
				return
			}
			go server.HandleConnection(conn)
		}
	}()

	clientTLS := &tls.Config{
		InsecureSkipVerify: true, // self-signed test cert has no SANs; pinned below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) != 1 || !certEqual(rawCerts[0], der) {
				return errors.New("presented certificate is not the pinned test certificate")
			}
			return nil
		},
	}
	client, err := NewQUICRPCClient(ln.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if cerr := client.Close(); cerr != nil {
			t.Logf("close client: %v", cerr)
		}
	})
	return client
}

func certEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestHandlerPanicDoesNotKillTheServer pins GOBLIN-DIV-041's contract: a
// panicking handler must cost its request, not the process. Before the
// recover existed, the panic escaped handleStream's goroutine and killed
// the whole test binary - which is exactly the node-crash primitive the
// entry describes.
func TestHandlerPanicDoesNotKillTheServer(t *testing.T) {
	client := panicHarness(t)

	var req, resp goblinv1.MembersRequest
	err := client.Call("test.Panic", &req, &resp)
	if err == nil {
		t.Fatal("panicking handler returned nil error")
	}
	var rpcErr *RPCCallError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *RPCCallError, got %T: %v", err, err)
	}
	if rpcErr.Code != goblinv1.RPCErrorCode_RPC_ERROR_CODE_INTERNAL {
		t.Errorf("code = %v, want INTERNAL", rpcErr.Code)
	}

	// The server must still answer on a fresh stream: the panic cost one
	// request, not the process.
	if err := client.Call("test.OK", &req, &resp); err != nil {
		t.Fatalf("follow-up call after handler panic: %v", err)
	}
}
