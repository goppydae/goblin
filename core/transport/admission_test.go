// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/goppydae/goblin/core/transport"
)

// GOBLIN-DIV-051's gate: phase-aware admission.
//
// The property is not "a connection is refused" - that already happened,
// and it is why this was a defect rather than a gap. The property is that
// the refusal CARRIES WHICH KIND it is, because not-ready-yet is
// retryable and nothing-is-listening is not. A peer that cannot tell them
// apart treats a transient phase skew as a hard join failure, which is
// what stopped clusters forming and surfaced as a placement timeout in
// whichever suite happened to be running.

// newListenerWithReadiness builds a listener whose phase readiness the
// test controls. It reuses testListener from listener_test.go, so the
// dial here pins the listener's certificate exactly as every other
// transport test does.
func newListenerWithReadiness(t *testing.T, ready func() bool) *testListener {
	t.Helper()
	cert, err := transport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	l, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
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
	return &testListener{SharedListener: l, certDER: cert.Certificate[0]}
}

// refusalOf dials alpn and returns the error the peer closed with. A
// refusal can arrive on the dial or on the first read, so both are tried.
func refusalOf(t *testing.T, tl *testListener, alpn string) error {
	t.Helper()
	conn, err := tl.dialALPN(t, alpn)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, aerr := conn.AcceptStream(ctx)
	_ = conn.CloseWithError(0, "")
	return aerr
}

func appCode(t *testing.T, err error) quic.ApplicationErrorCode {
	t.Helper()
	var appErr *quic.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("error is not a QUIC application error: %v", err)
	}
	return appErr.ErrorCode
}

// A cluster ALPN dialled before the planes register is refused as CLUSTER
// NOT READY, distinguishable from the permanent refusal.
func TestClusterALPNBeforePhase4IsRefusedAsNotReady(t *testing.T) {
	var ready atomic.Bool // stays false: this node has not finished booting

	for _, alpn := range []string{transport.ALPNSerfQUIC, transport.ALPNRaftQUIC, transport.ALPNGoblinRPC} {
		t.Run(alpn, func(t *testing.T) {
			tl := newListenerWithReadiness(t, ready.Load)
			err := refusalOf(t, tl, alpn)
			if err == nil {
				t.Fatal("a cluster ALPN was served before the cluster stack registered it")
			}
			got := appCode(t, err)
			if got == transport.CodeALPNNotServing {
				t.Fatalf("refused with CodeALPNNotServing, which means NEVER. A peer reads that "+
					"as fatal and abandons the join, so a node dialling during another node's "+
					"boot never forms a cluster with it (GOBLIN-DIV-051). Want "+
					"CodeClusterNotReady (%#x)", transport.CodeClusterNotReady)
			}
			if got != transport.CodeClusterNotReady {
				t.Fatalf("refused with %#x, want CodeClusterNotReady (%#x)",
					got, transport.CodeClusterNotReady)
			}
			// The dial path branches on this predicate, so it must agree
			// with the code the listener actually sent.
			if !transport.IsClusterNotReady(err) {
				t.Fatal("IsClusterNotReady rejected a refusal the listener sent as " +
					"cluster-not-ready; the dialer would not retry")
			}
		})
	}
}

// Once every plane is registered, an unregistered ALPN means what it
// says: never. Without this the two codes would differ in name only - a
// listener that always says "not ready" is as uninformative as one that
// always says "not serving", just in the other direction.
func TestUnregisteredALPNAfterBootIsPermanent(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true) // boot complete

	tl := newListenerWithReadiness(t, ready.Load)
	err := refusalOf(t, tl, transport.ALPNSerfQUIC)
	if err == nil {
		t.Fatal("an unregistered ALPN was served")
	}
	if got := appCode(t, err); got != transport.CodeALPNNotServing {
		t.Fatalf("refused with %#x after boot, want CodeALPNNotServing (%#x): a peer must "+
			"not retry forever against a plane that will never register",
			got, transport.CodeALPNNotServing)
	}
	if transport.IsClusterNotReady(err) {
		t.Fatal("IsClusterNotReady accepted a permanent refusal; the dialer would retry until timeout")
	}
}

// A non-cluster ALPN is never "not ready", whatever the phase. gapi-quic
// registers late too, but serving it earlier is a redesign of what its
// handler depends on rather than a wait, so promising a peer "not yet"
// would promise something waiting does not deliver.
func TestNonClusterALPNIsNeverDeferred(t *testing.T) {
	var ready atomic.Bool // not ready

	tl := newListenerWithReadiness(t, ready.Load)
	err := refusalOf(t, tl, transport.ALPNGapiQUIC)
	if err == nil {
		t.Fatal("an unregistered ALPN was served")
	}
	if got := appCode(t, err); got != transport.CodeALPNNotServing {
		t.Fatalf("gapi-quic refused with %#x, want CodeALPNNotServing (%#x)",
			got, transport.CodeALPNNotServing)
	}
	if transport.IsClusterALPN(transport.ALPNGapiQUIC) {
		t.Fatal("gapi-quic is classified as a cluster ALPN; waiting will not make it serve")
	}
}

// The predicate is required. Whichever default a nil one implied would be
// wrong somewhere: "always ready" reintroduces the defect, "never ready"
// makes every peer retry until it times out.
func TestListenerRefusesToBuildWithoutAReadinessPredicate(t *testing.T) {
	cert, err := transport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	ln, err := transport.NewSharedListener("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil)
	if err == nil {
		_ = ln.Close()
		t.Fatal("a listener was built with no readiness predicate")
	}
}
