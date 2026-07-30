package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	goblintransport "github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/supervisor"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
)

// newTestRPCServer starts a real QUIC listener speaking the goblin-rpc
// ALPN and serves it with supervisor.QUICRPCServer, so the cli client's
// roundTrip exercises the same wire path the CLI uses in production
// (dispatch classification happens server-side in internal/supervisor,
// not in a mock). It returns the listener address and the exact server
// certificate DER, so the test dial can pin it rather than skip
// verification outright (the self-signed test cert has no SANs, so
// pool/hostname verification cannot apply - same constraint noted in
// core/transport/listener_test.go).
func newTestRPCServer(t *testing.T) (addr string, certDER []byte, server *supervisor.QUICRPCServer) {
	t.Helper()

	cert, err := goblintransport.GenerateInsecureSelfSignedCert()
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}

	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{goblintransport.ALPNGoblinRPC},
	}, &quic.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	server = supervisor.NewQUICRPCServer()

	go func() {
		for {
			conn, aerr := ln.Accept(context.Background())
			if aerr != nil {
				return
			}
			go server.HandleConnection(conn)
		}
	}()

	return ln.Addr().String(), cert.Certificate[0], server
}

// dialPinned opens the same kind of connection cli.NewQUICRPCClient
// would, except verification pins the exact test certificate byte-for-
// byte instead of skipping it: production's TLSConfig plumbing (CAFile
// or InsecureSkipVerify) has no hook for a callback pin, so the dial is
// built directly here. The resulting *QUICRPCClient is otherwise
// identical to one NewQUICRPCClient would hand back - same struct,
// same Call/roundTrip code under test - only the certificate trust
// decision differs.
func dialPinned(t *testing.T, addr string, certDER []byte) *QUICRPCClient {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], certDER) {
				return errors.New("server certificate does not match the pinned test certificate")
			}
			return nil
		},
		NextProtos: []string{goblintransport.ALPNGoblinRPC},
	}, &quic.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return &QUICRPCClient{conn: conn}
}

// TestQUICRPCClient_Call_SurfacesTypedError exercises the unified error
// path end to end: a real dispatch on a real supervisor.QUICRPCServer,
// decoded by the cli client's roundTrip into the same *RPCCallError
// type internal/supervisor's own client builds (batch E, item 2).
//
// Hypothesis: both a handler-returned error and a dispatch against an
// unregistered method arrive at the cli caller as *supervisor.RPCCallError
// with the code the server classified, retrievable via errors.As.
// Disproof: either case surfacing a plain fmt.Errorf (the pre-unification
// behavior) or the wrong RPCErrorCode.
func TestQUICRPCClient_Call_SurfacesTypedError(t *testing.T) {
	addr, certDER, server := newTestRPCServer(t)

	server.RegisterHandler("test.HandlerError", func(payload []byte) ([]byte, error) {
		return nil, fmt.Errorf("%w: bad field", supervisor.ErrInvalidRequest)
	})

	client := dialPinned(t, addr, certDER)
	defer func() { _ = client.Close() }()

	tests := []struct {
		name   string
		method string
		want   goblinv1.RPCErrorCode
	}{
		{"handler error", "test.HandlerError", goblinv1.RPCErrorCode_RPC_ERROR_CODE_INVALID_REQUEST},
		{"unknown method", "test.NoSuchMethod", goblinv1.RPCErrorCode_RPC_ERROR_CODE_NOT_FOUND},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req, resp goblinv1.MembersRequest
			callErr := client.Call(tc.method, &req, &resp)
			if callErr == nil {
				t.Fatalf("Call(%s) succeeded, want error", tc.method)
			}

			var rpcErr *supervisor.RPCCallError
			if !errors.As(callErr, &rpcErr) {
				t.Fatalf("Call(%s) err = %v, want errors.As target *supervisor.RPCCallError", tc.method, callErr)
			}
			if rpcErr.Code != tc.want {
				t.Errorf("Call(%s) code = %v, want %v", tc.method, rpcErr.Code, tc.want)
			}
		})
	}
}
