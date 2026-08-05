// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

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
	"strings"
	"testing"
	"time"

	"github.com/goppydae/goblin/core/capability"
	goblintransport "github.com/goppydae/goblin/core/transport"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
)

// servingNode starts a real QUIC RPC server carrying the scheduler
// handlers for one node's revocation filter, and returns a client
// dialled to it.
//
// A real listener rather than a direct call: the property under test is
// that a node repairs itself from ANOTHER node, and collapsing the
// transport would leave the thing being asserted partly in the test.
// Cert handling follows the pinned-DER pattern used by the panic
// harness - the self-signed cert has no SANs, so the client skips chain
// verification and pins the exact certificate.
func servingNode(t *testing.T, revs *capability.Revocations) *QUICRPCClient {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "revocation-sync-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	ln, err := quic.Listen(udpConn, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{goblintransport.ALPNGoblinRPC},
	}, nil)
	if err != nil {
		t.Fatalf("quic listen: %v", err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Logf("close listener: %v", cerr)
		}
	})

	server := NewQUICRPCServer()
	RegisterSchedulerHandlers(server, &SchedulerRPC{revocations: revs})

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

	client, err := NewQUICRPCClient(ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, // self-signed test cert has no SANs; pinned below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) != 1 || !certEqual(rawCerts[0], der) {
				return errors.New("presented certificate is not the pinned test certificate")
			}
			return nil
		},
	})
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

// syncFrom builds a loop that repairs local from exactly one peer,
// reached over the given client.
func syncFrom(local *capability.Revocations, client *QUICRPCClient) *revocationSync {
	return &revocationSync{
		revocations: local,
		peers:       func() []string { return []string{"peer"} },
		exchange: func(_ context.Context, _ string, req *goblinv1.SyncRevocationsRequest) (*goblinv1.SyncRevocationsResponse, error) {
			var resp goblinv1.SyncRevocationsResponse
			if err := client.Call("SchedulerRPC.SyncRevocations", req, &resp); err != nil {
				return nil, err
			}
			return &resp, nil
		},
		pick:   func(int) int { return 0 },
		logger: quietLogger(),
	}
}

// TestAntiEntropy_RepairsANodeThatNeverSawTheDelta is what closes
// GOBLIN-DIV-057.
//
// Node A revokes a token. Node B never receives the broadcast - it was
// partitioned, restarting, or joined afterwards - and no delta ever
// reaches it. After one anti-entropy round B refuses that token at the
// checkpoint boundary, which is the only path where a capability token
// crosses a trust boundary and the only place the refusal matters.
//
// No delta test can demonstrate this property, which is why the entry
// named it as the gate.
func TestAntiEntropy_RepairsANodeThatNeverSawTheDelta(t *testing.T) {
	nodeB := capability.NewRevocations()
	authorizeOnB, token, inst, tokenID := checkpointFixture(t, nodeB)

	// Control first. Without it "refused" proves nothing: a fixture that
	// never authorized would pass for the wrong reason.
	if err := authorizeOnB(token, inst); err != nil {
		t.Fatalf("token should authorize on B before anything is revoked: %v", err)
	}

	// A revokes. Nothing carries it to B - no bus, no delta, no shared
	// filter object.
	nodeA := capability.NewRevocations()
	nodeA.Revoke(tokenID)

	if err := authorizeOnB(token, inst); err != nil {
		t.Fatalf("B refused before syncing, so the test cannot show sync caused anything: %v", err)
	}

	if err := syncFrom(nodeB, servingNode(t, nodeA)).syncOnce(context.Background()); err != nil {
		t.Fatalf("anti-entropy round: %v", err)
	}

	err := authorizeOnB(token, inst)
	if err == nil {
		t.Fatal("B accepted a token A revoked: anti-entropy did not repair the missed delta")
	}
	if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("B refused for the wrong reason: %v", err)
	}
}

// The generation-boundary case - A revokes late in a window, the window
// rolls before B reconnects, and the repair still has to reach B - is
// NOT retested here. Driving it needs a settable clock, and the clock
// is unexported in core/capability, where
// TestSync_RepairsARevocationFromThePreviousGeneration already pins it
// against the real Snapshot and Ingest. Exporting a clock hook so this
// test could restate it would put test-only surface on a production
// type to prove something already proven.
