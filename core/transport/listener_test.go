package transport_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/goppydae/goblin/core/transport"
	"github.com/quic-go/quic-go"
)

// testListener carries the listener plus the exact server certificate,
// so test dials verify by certificate pinning (the self-signed test
// cert has no SANs, so pool-based verification cannot apply).
type testListener struct {
	*transport.SharedListener
	certDER []byte
}

func newSharedListener(t *testing.T) *testListener {
	t.Helper()
	cert, err := transport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	l, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("NewSharedListener: %v", err)
	}
	t.Cleanup(func() {
		if cerr := l.Close(); cerr != nil {
			t.Logf("close listener: %v", cerr)
		}
	})
	return &testListener{SharedListener: l, certDER: cert.Certificate[0]}
}

func (tl *testListener) dialALPN(t *testing.T, alpn string) (*quic.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return quic.DialAddr(ctx, tl.Addr().String(), &tls.Config{
		// Default chain/hostname verification is replaced by an exact
		// pin of the listener certificate generated in this test.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], tl.certDER) {
				return errors.New("server certificate does not match the pinned test certificate")
			}
			return nil
		},
		NextProtos: []string{alpn},
	}, &quic.Config{EnableDatagrams: true})
}

// Connections route to the adapter registered for their negotiated ALPN.
func TestSharedListener_RoutesByALPN(t *testing.T) {
	l := newSharedListener(t)
	raftCh, err := l.Register(transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("register raft: %v", err)
	}
	serfCh, err := l.Register(transport.ALPNSerfQUIC)
	if err != nil {
		t.Fatalf("register serf: %v", err)
	}

	rc, err := l.dialALPN(t, transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("dial raft-quic: %v", err)
	}
	defer func() { _ = rc.CloseWithError(0, "done") }()
	sc, err := l.dialALPN(t, transport.ALPNSerfQUIC)
	if err != nil {
		t.Fatalf("dial serf-quic: %v", err)
	}
	defer func() { _ = sc.CloseWithError(0, "done") }()

	for name, ch := range map[string]<-chan *quic.Conn{"raft": raftCh, "serf": serfCh} {
		select {
		case conn := <-ch:
			got := conn.ConnectionState().TLS.NegotiatedProtocol
			want := map[string]string{"raft": transport.ALPNRaftQUIC, "serf": transport.ALPNSerfQUIC}[name]
			if got != want {
				t.Errorf("%s channel received ALPN %q, want %q", name, got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no connection routed to %s adapter", name)
		}
	}
}

// A registered-in-TLS but unrouted ALPN (its adapter has not attached -
// phase-aware admission) is refused with a close, not silently parked.
func TestSharedListener_UnroutedALPNRefused(t *testing.T) {
	l := newSharedListener(t)
	// raft-quic is advertised by the listener but no adapter registered.
	conn, err := l.dialALPN(t, transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseWithError(0, "done") }()

	// The refusal surfaces as an error on the next stream operation.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := conn.AcceptStream(ctx); err == nil {
		t.Fatal("unrouted ALPN connection was not closed")
	}
}

// An ALPN outside the registry fails at the TLS handshake: the listener
// only advertises registry rows.
func TestSharedListener_UnknownALPNFailsHandshake(t *testing.T) {
	l := newSharedListener(t)
	if _, err := l.dialALPN(t, "totally-unknown"); err == nil {
		t.Fatal("handshake with unknown ALPN succeeded")
	}
}

// Double registration of one ALPN is a wiring bug and must error.
func TestSharedListener_DoubleRegisterRejected(t *testing.T) {
	l := newSharedListener(t)
	if _, err := l.Register(transport.ALPNRaftQUIC); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := l.Register(transport.ALPNRaftQUIC); err == nil {
		t.Fatal("double register accepted")
	}
	// Registering an ALPN outside the registry is a wiring bug too.
	if _, err := l.Register("not-in-registry"); err == nil {
		t.Fatal("register of unknown ALPN accepted")
	}
}

// Datagram support is negotiated on the shared listener (serf needs it).
func TestSharedListener_DatagramsEnabled(t *testing.T) {
	l := newSharedListener(t)
	ch, err := l.Register(transport.ALPNSerfQUIC)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	client, err := l.dialALPN(t, transport.ALPNSerfQUIC)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.CloseWithError(0, "done") }()

	select {
	case server := <-ch:
		if err := server.SendDatagram([]byte("ping")); err != nil {
			t.Fatalf("server datagram send: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msg, err := client.ReceiveDatagram(ctx)
		if err != nil {
			t.Fatalf("client datagram receive: %v", err)
		}
		if string(msg) != "ping" {
			t.Fatalf("datagram payload = %q, want ping", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connection not routed")
	}
}
