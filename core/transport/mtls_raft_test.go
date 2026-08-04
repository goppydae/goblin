// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/goppydae/goblin/core/transport"
	"github.com/quic-go/quic-go"
)

// GOBLIN-DIV-043: the raft plane's trust boundary is the TLS handshake.
//
// Every operator key registry guard runs inside FSM Apply, and
// InstallSnapshot does not go through Apply - FSM.Restore installs the
// registry straight from the snapshot payload, and a snapshot is not a
// signed object. So whether a hostile peer can seed itself as the
// cluster's root of trust reduces entirely to whether it can open a
// raft-quic connection at all.
//
// These tests assert that reduction holds on the real SharedListener,
// not on a hand-rolled tls.Config: the listener goblind actually binds
// is the thing that has to refuse.

// certAuthority is a throwaway CA plus the leaf issuance the mTLS tests
// need. Certificates are generated per-test rather than fixtured, so a
// stale fixture cannot make a refusal test pass for the wrong reason.
type certAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCertAuthority(t *testing.T) *certAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "goblin-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &certAuthority{cert: cert, key: key, pool: pool}
}

// issue mints a leaf good for both client and server auth, matching the
// certificates a real deployment hands its peers.
func (ca *certAuthority) issue(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth,
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: mustParse(t, der)}
}

func mustParse(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return c
}

// mtlsListener binds a SharedListener configured exactly as
// supervisor.Run configures it when ca-file is set: client certificates
// required and verified against the CA pool.
func mtlsListener(t *testing.T, ca *certAuthority) *transport.SharedListener {
	t.Helper()
	l, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{ca.issue(t, "goblin-node")},
		ClientCAs:    ca.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		RootCAs:      ca.pool,
		MinVersion:   tls.VersionTLS13,
	}, alwaysReady)
	if err != nil {
		t.Fatalf("NewSharedListener: %v", err)
	}
	t.Cleanup(func() {
		if cerr := l.Close(); cerr != nil {
			t.Logf("close listener: %v", cerr)
		}
	})
	return l
}

// The two waits are deliberately asymmetric, and the asymmetry is the
// whole point.
//
// admitWait bounds an event that MUST happen, so a generous budget costs
// nothing when the code is correct and only delays reporting a genuine
// break. Five seconds was not generous enough: this test failed on CI
// (run 30725365624) on a 4-vCPU runner under -race, where quic-go also
// warns it could not raise the UDP receive buffer past 2 MiB. Thirty
// never hides a defect - a listener that refuses a valid peer refuses it
// at any timeout.
//
// refuseWait bounds a NON-event, so it cannot be generous: every second
// spent proving nothing arrived is a second added to a passing run. It
// is short on purpose, and the cost is that a refusal test would also
// pass against a listener that is merely very slow. That gap is covered
// by the admit test proving the routing path works at all in the same
// package - which is why the admit test going flaky mattered more than
// its own failure.
const (
	admitWait  = 30 * time.Second
	refuseWait = 2 * time.Second
)

// captureAcceptLog redirects slog for the duration of one test and
// returns what the accept loop said.
//
// GOBLIN-DIV-060. This test has failed on CI four times across three
// occasions, always identically: "never reached the raft plane" at the
// admitWait ceiling, on diffs that cannot touch the mTLS or raft path -
// a VERSION string, a licence-header sweep, a .gitignore. Each time the
// message was the same and told nobody anything, so each occurrence cost
// a signature comparison to establish it was the flake again.
//
// The listener already knows which of five things happened: it accepted
// and routed (silent), accepted and refused for ALPN, refused because
// the cluster is not ready, dropped a backlogged adapter, or failed to
// accept at all. Four of those five are logged and this test observed
// none of them. That is the gap.
//
// SILENCE IS ITSELF THE DIAGNOSIS, and the useful one: it means the
// connection was never accepted, which is a different defect from being
// accepted and turned away. Two prior fixes for this symptom - commit
// 00b6ad0 and the UDP receive-buffer step in ci.yml - both aimed at the
// second story without evidence for it, and neither closed the entry.
//
// Tests in this package do not call t.Parallel, so swapping the global
// default is safe; the handler is restored by t.Cleanup regardless of
// outcome. The buffer is mutex-guarded because the accept loop writes
// from its own goroutine while the test reads.
func captureAcceptLog(t *testing.T) func() string {
	t.Helper()
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&lockedWriter{mu: &mu, w: &buf},
		&slog.HandlerOptions{Level: slog.LevelDebug},
	)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if buf.Len() == 0 {
			return "  (nothing: the accept loop never logged, so the " +
				"connection was never accepted - it was not accepted and refused)"
		}
		return buf.String()
	}
}

// lockedWriter serialises writes from the accept loop goroutine against
// reads from the test goroutine. bytes.Buffer is not safe for that on
// its own, and -race is enabled here.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// dialRaft attempts a raft-plane connection. Its error is reported for
// diagnostics only and is deliberately NOT the assertion: a QUIC client
// can complete its handshake, open a stream and write, all buffered
// locally, before the server's rejection alert arrives - so a dial that
// "succeeds" client-side proves nothing about whether the peer was
// admitted. The authoritative question is answered server-side by
// reachedRaftPlane.
func dialRaft(t *testing.T, addr string, ca *certAuthority, client *tls.Certificate) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), admitWait)
	defer cancel()

	cfg := &tls.Config{
		RootCAs:    ca.pool,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{transport.ALPNRaftQUIC},
	}
	if client != nil {
		cfg.Certificates = []tls.Certificate{*client}
	}
	conn, err := quic.DialAddr(ctx, addr, cfg, &quic.Config{})
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, serr := conn.OpenStreamSync(ctx)
	if serr != nil {
		return serr
	}
	if _, werr := stream.Write([]byte{0}); werr != nil {
		return werr
	}
	return nil
}

// reachedRaftPlane reports whether a connection was routed to the raft
// ALPN adapter - which is exactly what "an unauthenticated peer can send
// InstallSnapshot" means. The listener only routes after the TLS
// handshake completes and an ALPN is negotiated, so a peer refused for
// its client certificate can never appear here.
func reachedRaftPlane(ch <-chan *quic.Conn, within time.Duration) bool {
	select {
	case conn := <-ch:
		if conn != nil {
			_ = conn.CloseWithError(0, "")
		}
		return true
	case <-time.After(within):
		return false
	}
}

// An unauthenticated peer cannot reach the raft plane at all. This is
// the demonstration GOBLIN-DIV-043's exit asks for: refused at the
// transport, before any raft message - InstallSnapshot included - is
// ever parsed.
func TestSharedListener_RefusesRaftPeerWithoutClientCert(t *testing.T) {
	ca := newCertAuthority(t)
	l := mtlsListener(t, ca)
	raftCh, err := l.Register(transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("register raft alpn: %v", err)
	}

	t.Logf("dial without client cert returned: %v", dialRaft(t, l.Addr().String(), ca, nil))
	if reachedRaftPlane(raftCh, refuseWait) {
		t.Fatal("a peer presenting no client certificate reached the raft plane; " +
			"it could send InstallSnapshot and seed the operator key registry " +
			"through FSM.Restore, which does not pass through Apply (GOBLIN-DIV-043)")
	}
}

// A peer holding a certificate from an unrelated CA is refused too. The
// test above alone would pass against a listener that merely demanded
// SOME certificate without verifying who signed it.
func TestSharedListener_RefusesRaftPeerWithForeignCert(t *testing.T) {
	ca := newCertAuthority(t)
	l := mtlsListener(t, ca)
	raftCh, err := l.Register(transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("register raft alpn: %v", err)
	}

	foreign := newCertAuthority(t)
	cert := foreign.issue(t, "attacker")
	t.Logf("dial with foreign cert returned: %v", dialRaft(t, l.Addr().String(), ca, &cert))
	if reachedRaftPlane(raftCh, refuseWait) {
		t.Fatal("a peer with a certificate from an unrelated CA reached the raft plane")
	}
}

// The positive case, without which both refusals above would pass
// against a listener that refuses everything - including a real peer.
func TestSharedListener_AdmitsRaftPeerWithIssuedCert(t *testing.T) {
	acceptLog := captureAcceptLog(t)

	ca := newCertAuthority(t)
	l := mtlsListener(t, ca)
	raftCh, err := l.Register(transport.ALPNRaftQUIC)
	if err != nil {
		t.Fatalf("register raft alpn: %v", err)
	}

	cert := ca.issue(t, "goblin-peer")
	dialStart := time.Now()
	if err := dialRaft(t, l.Addr().String(), ca, &cert); err != nil {
		t.Fatalf("a peer with a CA-issued client certificate was refused: %v\n"+
			"accept loop said:\n%s", err, acceptLog())
	}
	dialTook := time.Since(dialStart)

	routeStart := time.Now()
	routed := reachedRaftPlane(raftCh, admitWait)
	routeTook := time.Since(routeStart)

	// The margin, recorded on EVERY run including green ones, which is
	// what GOBLIN-DIV-060's exit asks for: the artifact IS the
	// measurement and has to be regenerated rather than asserted once.
	// Before this, the failures were known to sit at the ceiling and the
	// passing runs measured nothing at all - so nobody could say whether
	// a healthy route takes 2% of the budget or 60% of it, which is the
	// difference between a thin margin and an unrelated defect.
	t.Logf("margin: dial %s, route %s, budget %s (%.2f%% of budget used)",
		dialTook.Round(time.Millisecond),
		routeTook.Round(time.Millisecond),
		admitWait,
		float64(routeTook)/float64(admitWait)*100)

	if !routed {
		t.Fatalf("a peer with a CA-issued client certificate never reached "+
			"the raft plane within %s (dial itself took %s).\n"+
			"GOBLIN-DIV-060: what the accept loop did is below. Silence "+
			"means the connection was never accepted at all, which is NOT "+
			"the same defect as being accepted and refused.\n%s",
			admitWait, dialTook.Round(time.Millisecond), acceptLog())
	}
}
