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

	// A SET, NOT A SLOT (GAPI-DIV-106). This was one `conn *quic.Conn`
	// that handleConn assigned unconditionally, so it was
	// last-writer-wins with no eviction and no log: a second client
	// silently displaced the first, and only the newest peer ever
	// received a server-initiated push.
	//
	// It went unnoticed because request/response rides a
	// CLIENT-opened stream and was unaffected, while the displaced
	// subscriber simply stopped hearing - no error at either end. A
	// single operator running the TUI already holds two of these
	// (core/client.New dials once for the status poller; core/tui
	// opens a second client per lifecycle action), so serving one peer
	// was never enough and refusing the second was never an option.
	//
	// Keyed by connection because that is the identity the lifecycle
	// already has: handleConn owns exactly one, adds it on entry and
	// removes it when its AcceptStream loop ends, which IS the death
	// of that connection.
	peers map[*quic.Conn]struct{}
	mu    sync.Mutex

	onRemote func(eventbus.Event[*anypb.Any])
}

// PeerCount reports how many connections this transport would address.
// Exported for tests: "the publish reached both peers" is only a
// meaningful assertion once "both peers are attached" can be awaited
// rather than slept on.
func (q *QUIC) PeerCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.peers)
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
	q := &QUIC{listener: ln, peers: make(map[*quic.Conn]struct{})}
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
	// The client does not seed the set itself: handleConn adds the
	// connection and removes it when it dies, so a client's own peer has
	// exactly the lifecycle a server's does and there is one place that
	// owns membership.
	q := &QUIC{peers: make(map[*quic.Conn]struct{})}
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
	q.peers[conn] = struct{}{}
	q.mu.Unlock()

	// REMOVAL BELONGS HERE AND NOWHERE ELSE. This loop already runs
	// until AcceptStream errors, and that error IS the death of the
	// connection, so the existing lifecycle already had the right
	// removal point. Evicting on a failed PUBLISH instead would make a
	// slow peer indistinguishable from a dead one.
	defer func() {
		q.mu.Lock()
		delete(q.peers, conn)
		q.mu.Unlock()
	}()

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

// ---- Publish ----

func (q *QUIC) PublishRemote(ctx context.Context, e eventbus.Event[*anypb.Any]) error {
	// Snapshot under the mutex. Sending while holding it would let one
	// unresponsive peer block every other publisher.
	q.mu.Lock()
	peers := make([]*quic.Conn, 0, len(q.peers))
	for c := range q.peers {
		peers = append(peers, c)
	}
	q.mu.Unlock()

	if len(peers) == 0 {
		// An EMPTINESS check now, where it was a nil check; GAPI-DIV-095's
		// reasoning is unchanged. NOT io.ErrUnexpectedEOF, which asserts
		// that a read ended before it should have. Nothing was read and
		// nothing went wrong: there is simply nobody to send to.
		return eventbus.ErrNoPeer
	}

	// ONE GOROUTINE PER PEER. A publish reaching some peers and not
	// others must not depend on their order, so no peer's send is
	// sequenced behind another's timeout.
	for _, conn := range peers {
		q.publishTo(ctx, conn, e)
	}

	return nil
}

// publishTo sends one event to one peer. The body is unchanged from when
// this transport addressed a single connection - the defect was never in
// how a frame is written, only in how many peers were reachable.
func (q *QUIC) publishTo(ctx context.Context, conn *quic.Conn, e eventbus.Event[*anypb.Any]) {
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
		// Event.Broadcast is deliberately absent, and it is now absent
		// from the Event too (GAPI-DIV-106). It was a flag with no
		// receiver: this transport addressed ONE peer, so "broadcast"
		// and "publish" were the same operation and the two bus arms
		// called the same code. With a peer set, a remote publish IS to
		// every peer, so the flag has nothing left to select and the
		// distinction it encoded never existed on the wire.
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
	// EVERY peer, not the most recent one. When this was a single slot,
	// closing "the connection" and closing "all connections" were the
	// same statement; with a set they are not, and a Close that shut
	// only one would leak the rest - silently, since nothing here
	// reports a per-peer failure.
	//
	// The map is cleared rather than left with dead entries so that
	// PeerCount and the ErrNoPeer check agree with reality immediately,
	// instead of waiting for each handleConn goroutine to notice its
	// AcceptStream has failed and remove itself.
	for c := range q.peers {
		_ = c.CloseWithError(0, "shutdown")
		delete(q.peers, c)
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
