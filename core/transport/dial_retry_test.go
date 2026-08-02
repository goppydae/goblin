package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// The second half of GOBLIN-DIV-051.
//
// Distinguishing the refusal codes changes nothing on its own: before
// this, no code anywhere inspected the QUIC application error, so a
// joining peer treated every refusal identically and a renamed refusal
// would have been renamed silence. These tests are the reason the entry's
// exit was extended - closing on the listener half alone would have
// certified a capability while the flake it causes carried on.
//
// An internal test because dialWithClusterNotReadyRetry is unexported: it
// is deliberately not part of the package's surface, since "wait for the
// peer to finish booting" is a decision the dial paths make, not one a
// caller should be able to opt out of.

func retryTestListener(t *testing.T, ready func() bool) (*SharedListener, *tls.Config) {
	t.Helper()
	cert, err := GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	l, err := NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, ready)
	if err != nil {
		t.Fatalf("NewSharedListener: %v", err)
	}
	t.Cleanup(func() {
		if cerr := l.Close(); cerr != nil {
			t.Logf("close listener: %v", cerr)
		}
	})
	der := cert.Certificate[0]
	clientTLS := &tls.Config{
		// Chain and hostname verification are REPLACED by an exact pin of
		// the certificate this test generated, not disabled.
		InsecureSkipVerify: true, //nolint:gosec // pinned below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], der) {
				return errors.New("server certificate does not match the pinned test certificate")
			}
			return nil
		},
		NextProtos: []string{ALPNSerfQUIC},
		MinVersion: tls.VersionTLS13,
	}
	return l, clientTLS
}

// A dial against a peer still booting waits and succeeds once that peer
// registers the plane. This is the flake, reproduced and fixed: node-2
// dialling node-1 during node-1's Phase 4 window.
func TestDialWaitsOutAPeerThatIsNotClusterReady(t *testing.T) {
	var ready atomic.Bool // node-1 has not reached Phase 4

	l, clientTLS := retryTestListener(t, ready.Load)

	// The peer finishes booting shortly after the dial begins.
	go func() {
		time.Sleep(600 * time.Millisecond)
		if _, err := l.Register(ALPNSerfQUIC); err != nil {
			t.Errorf("register serf plane: %v", err)
		}
		ready.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := dialWithClusterNotReadyRetry(ctx, l.Addr().String(), clientTLS,
		&quic.Config{EnableDatagrams: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("dial gave up on a peer that was still booting: %v.\n"+
			"This is the join failure that stopped clusters forming: the refusal was "+
			"transient and the dialer treated it as final (GOBLIN-DIV-051)", err)
	}
	_ = conn.CloseWithError(0, "")

	// ASSERT THE WAIT, not just the success. An earlier version of this
	// test returned in 10ms and passed - because quic.DialAddr hands back
	// a live connection and the refusal lands on that connection a moment
	// LATER, so a dial that never watched for it "succeeded" with a
	// doomed connection. Without this bound the test cannot tell a retry
	// from that.
	if elapsed < 500*time.Millisecond {
		t.Fatalf("dial returned in %v, before the peer registered its plane at 600ms: "+
			"it did not wait, it got a connection that was about to be closed", elapsed)
	}
}

// A permanent refusal is NOT retried. Without this the fix would trade a
// fast wrong answer for a slow one: every dial to a plane that will never
// register would burn the caller's whole timeout.
func TestDialDoesNotRetryAPermanentRefusal(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true) // booted, and serf is simply not registered

	l, clientTLS := retryTestListener(t, ready.Load)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := dialWithClusterNotReadyRetry(ctx, l.Addr().String(), clientTLS,
		&quic.Config{EnableDatagrams: true})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("dial succeeded against an unregistered plane")
	}
	if IsClusterNotReady(err) {
		t.Fatalf("a permanent refusal was classified as retryable: %v", err)
	}
	// Generous, but decisive: retrying would consume the 5s budget.
	if elapsed > 2*time.Second {
		t.Fatalf("dial took %v against a permanent refusal; it retried when it should "+
			"have failed immediately", elapsed)
	}
}

// A local application error with the same code is not a peer's refusal
// and must not trigger a redial of anyone.
func TestIsClusterNotReadyIgnoresLocalErrors(t *testing.T) {
	local := &quic.ApplicationError{Remote: false, ErrorCode: CodeClusterNotReady}
	if IsClusterNotReady(local) {
		t.Fatal("a LOCAL cluster-not-ready error was treated as a peer's refusal")
	}
	remote := &quic.ApplicationError{Remote: true, ErrorCode: CodeClusterNotReady}
	if !IsClusterNotReady(remote) {
		t.Fatal("a remote cluster-not-ready refusal was not recognised")
	}
	if IsClusterNotReady(errors.New("unrelated")) {
		t.Fatal("a non-QUIC error was treated as a refusal")
	}
}
