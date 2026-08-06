// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net"
	"os"
	"sync"
	"time"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	quic "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// TLSConfig holds TLS configuration for the client
type TLSConfig struct {
	// InsecureSkipVerify controls whether a client verifies the server's certificate chain and host name.
	InsecureSkipVerify bool
	// CAFile is the path to the CA certificate file.
	CAFile string
}

type QUIC struct {
	listener *quic.Listener
	conn     *quic.Conn
	mu       sync.Mutex

	onRemote func(eventbus.Event[*anypb.Any])
}

// ---- Constructors ----

func NewQUICServer(addr string, cert tls.Certificate) (*QUIC, error) {
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{ALPNGapiQUIC},
	}
	quicConfig := &quic.Config{
		KeepAlivePeriod: config.QUICStreamTimeout,
		MaxIdleTimeout:  config.QUICIdleTimeout,
	}
	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig)
	if err != nil {
		return nil, err
	}
	q := &QUIC{listener: ln}
	go q.acceptLoop(ln)
	return q, nil
}

func NewQUICClient(addr string, cert *tls.Certificate, tlsConfig TLSConfig) (*QUIC, error) {
	tlsConf, err := CreateClientTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}

	if cert != nil {
		tlsConf.Certificates = []tls.Certificate{*cert}
	}
	quicConfig := &quic.Config{
		KeepAlivePeriod: config.QUICStreamTimeout,
		MaxIdleTimeout:  config.QUICIdleTimeout,
	}
	conn, err := quic.DialAddr(context.Background(), addr, tlsConf, quicConfig)
	if err != nil {
		return nil, err
	}
	q := &QUIC{conn: conn}
	go q.handleConn(conn)
	return q, nil
}

// CreateClientTLSConfig builds a tls.Config from the provided settings
func CreateClientTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	tlsConf := &tls.Config{
		NextProtos: []string{ALPNGapiQUIC},
	}

	if cfg.InsecureSkipVerify {
		tlsConf.InsecureSkipVerify = true
	} else if cfg.CAFile != "" {
		// Load CA cert
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file: %w", err)
		}
		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA cert")
		}
		tlsConf.RootCAs = caPool
	}

	return tlsConf, nil
}

// Addr reports the address the listener ACTUALLY BOUND, which is not
// always the one it was asked for: ":0" resolves to a kernel-assigned
// port, and a configured hostname may resolve to something else. The
// daemon is the only party that knows this value, which is why it has
// to be published rather than re-derived by the client (GAPI-DIV-070).
//
// Empty for a client QUIC, which has no listener. Takes q.mu because
// Close() nils the field.
func (q *QUIC) Addr() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.listener == nil {
		return ""
	}
	return q.listener.Addr().String()
}

// ---- Server / Client loops ----

// acceptLoop takes the listener as an argument rather than reading the
// mutable q.listener field: Close() nils that field under q.mu, and an
// unlocked field read here races it. Close() closing the listener makes
// Accept return ErrServerClosed, which remains the shutdown signal.
func (q *QUIC) acceptLoop(ln *quic.Listener) {
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "quic listener started", logattr.Addr(ln.Addr().String()))
	var tempDelay time.Duration

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			// Check for intentional shutdown
			if errors.Is(err, quic.ErrServerClosed) {
				slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "quic listener closed")
				return
			}

			// Retry timeouts with exponential backoff (Temporary() is
			// deprecated: timeouts are the well-defined retryable class).
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "quic accept temporary error, retrying", logattr.Err(err), logattr.RetryIn(tempDelay))
				time.Sleep(tempDelay)
				continue
			}

			// Fatal error
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "quic accept fatal error", logattr.Err(err))
			return
		}

		// Reset temporary delay on success
		tempDelay = 0
		go q.handleConn(conn)
	}
}

func (q *QUIC) handleConn(conn *quic.Conn) {
	q.mu.Lock()
	q.conn = conn
	q.mu.Unlock()
	for {
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "accept stream failed", logattr.Err(err))
			return
		}
		go q.handleStream(s)
	}
}

func (q *QUIC) handleStream(s *quic.Stream) {
	// Read-side stream close in a handler goroutine: the terminus is a log.
	defer func() {
		if cerr := s.Close(); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close stream failed", logattr.Err(cerr))
		}
	}()

	var lenBuf [4]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "read length prefix failed", logattr.Err(err))
		return
	}
	n := binary.BigEndian.Uint32(lenBuf[:])

	data := make([]byte, n)
	if _, err := io.ReadFull(s, data); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "read payload failed", logattr.Err(err))
		return
	}

	var env protopkg.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "unmarshal envelope failed", logattr.Err(err))
		return
	}

	var payload *anypb.Any
	if env.Payload != nil {
		payload = env.Payload
	}

	// THE TOPIC IS OPAQUE (GAPI-DIV-102). Every routing value is read
	// from the field that declares it; nothing is re-derived from a
	// delimiter. The splitter that used to stand here recovered the
	// scope from the topic's first '/' segment, which made the pair
	// (scope, topic) unencodable without ambiguity and rested on an
	// unwritten taboo that no topic's first segment may ever be a scope
	// name.
	//
	// A PRE-102 SENDER'S FRAME ARRIVES SCOPELESS, AND THAT IS THE HARD
	// CUT (operator decision 39). It carries no scope field, so it
	// reaches the bus with Scope "" and is refused by the ingress
	// validation GAPI-DIV-100 added - loudly, with a logged reason.
	// There is deliberately no fallback re-splitting the topic: a
	// fallback re-deriving scope from a delimiter is precisely the
	// ambiguity this deletes.
	e := eventbus.Event[*anypb.Any]{
		ID:        env.Id,
		Scope:     env.Scope,
		Namespace: env.Namespace,
		Topic:     env.Topic,
		Source:    env.Source,
		Payload:   payload,
		Tags:      env.Tags,
	}

	if q.onRemote != nil {
		q.onRemote(e)
	}
}

// ---- Publish / Broadcast ----

func (q *QUIC) PublishRemote(ctx context.Context, e eventbus.Event[*anypb.Any]) error {
	q.mu.Lock()
	conn := q.conn
	q.mu.Unlock()
	if conn == nil {
		// NOT io.ErrUnexpectedEOF, which asserts that a read ended before
		// it should have. Nothing was read and nothing went wrong: there
		// is simply nobody to send to (GAPI-DIV-095).
		return eventbus.ErrNoPeer
	}

	// Async publish to prevent blocking on dead clients
	go func() {
		timeoutCtx, cancel := context.WithTimeout(ctx, config.QUICStreamTimeout)
		defer cancel()
		s, err := conn.OpenStreamSync(timeoutCtx)
		if err != nil {
			return
		}
		// Write-side close in a fire-and-forget publish goroutine: a close
		// error can mean the frame did not flush - log it loudly.
		defer func() {
			if cerr := s.Close(); cerr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelWarn, "close publish stream failed", logattr.Err(cerr))
			}
		}()

		// Every routing value the Event declares is written to the field
		// that declares it (GAPI-DIV-102). Namespace and tags were
		// declared on the Envelope and written by nobody, so they were
		// dropped on every publish in the system's life.
		//
		// Event.Broadcast is deliberately absent, and GAPI-DIV-106 is
		// why: this transport keeps ONE peer slot, so Broadcast and
		// PublishRemote are the same operation and there is no fan-out
		// for a receiver to act on. A flag here would encode a decision
		// no sender can make - and a field produced and never read can
		// be wrong indefinitely. The gap is a missing peer set, one
		// layer below the wire.
		env := &protopkg.Envelope{
			Id:        e.ID,
			Scope:     e.Scope,
			Namespace: e.Namespace,
			Topic:     e.Topic,
			Source:    e.Source,
			Type:      "event",
			Payload:   e.Payload,
			Tags:      e.Tags,
		}

		data, err := proto.Marshal(env)
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelError, "marshal envelope failed", logattr.Err(err))
			return
		}

		// Length prefix. The receiver caps messages far below this, but
		// the conversion itself must be provably in range.
		dataLen := len(data)
		if dataLen > math.MaxUint32 {
			slog.Default().LogAttrs(ctx, slog.LevelError, "envelope too large to frame", logattr.Bytes(dataLen))
			return
		}
		lenBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lenBuf, uint32(dataLen))
		if _, err := s.Write(lenBuf); err != nil {
			return
		}
		if _, err := s.Write(data); err != nil {
			return
		}
	}()

	return nil
}

func (q *QUIC) Broadcast(e eventbus.Event[*anypb.Any]) error {
	return q.PublishRemote(context.Background(), e)
}

func (q *QUIC) OnRemoteEvent(fn func(eventbus.Event[*anypb.Any])) { q.onRemote = fn }

func (q *QUIC) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	var err error
	if q.listener != nil {
		err = q.listener.Close()
		q.listener = nil
	}
	if q.conn != nil {
		_ = q.conn.CloseWithError(0, "shutdown")
		q.conn = nil
	}
	return err
}

// GenerateInsecureSelfSignedCert generates a self-signed cert for testing/insecure modes.
func GenerateInsecureSelfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
