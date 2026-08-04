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
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"time"

	"github.com/hashicorp/raft"
	"github.com/quic-go/quic-go"
)

// QUICStreamLayer implements raft.StreamLayer over QUIC. It does not
// own a listener: inbound connections arrive from the shared
// control-plane listener's ALPN router (GOBLIN-DIV-023); only the dial
// side opens connections, offering exactly the raft-quic ALPN.
type QUICStreamLayer struct {
	conns   <-chan *quic.Conn
	addr    net.Addr
	tlsConf *tls.Config
	closed  chan struct{}
}

// NewRoutedQUICStreamLayer builds the raft stream layer over routed
// connections. addr is the node's single advertised control-plane
// address - it becomes this server's raft address, so it must be
// dialable by peers.
func NewRoutedQUICStreamLayer(conns <-chan *quic.Conn, addr net.Addr, tlsConf *tls.Config) *QUICStreamLayer {
	return &QUICStreamLayer{
		conns:   conns,
		addr:    addr,
		tlsConf: tlsConf,
		closed:  make(chan struct{}),
	}
}

// Accept waits for the next routed raft-quic connection and surfaces
// its first stream as the raft net.Conn.
func (l *QUICStreamLayer) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn, ok := <-l.conns:
		if !ok {
			return nil, net.ErrClosed
		}
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			if cerr := conn.CloseWithError(1, "failed to accept stream"); cerr != nil {
				return nil, errors.Join(err, cerr)
			}
			return nil, err
		}
		return &QUICConn{
			Stream:  stream,
			Conn:    conn,
			reqAddr: conn.RemoteAddr(),
		}, nil
	}
}

// Close stops accepting routed connections. The shared listener stays
// open - it belongs to the supervisor, not this adapter.
func (l *QUICStreamLayer) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

// Addr returns the advertised control-plane address.
func (l *QUICStreamLayer) Addr() net.Addr {
	return l.addr
}

// Dial creates a new connection to the given address.
func (l *QUICStreamLayer) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	quicConf := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
	}

	clientTLS := l.tlsConf.Clone()
	if clientTLS == nil {
		clientTLS = &tls.Config{InsecureSkipVerify: true}
	}
	// The dial must offer exactly raft-quic: a multi-ALPN client offer
	// would let the shared listener negotiate some other plane.
	clientTLS.NextProtos = []string{ALPNRaftQUIC}

	conn, err := dialWithClusterNotReadyRetry(ctx, string(addr), clientTLS, quicConf)
	if err != nil {
		return nil, err
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		if cerr := conn.CloseWithError(1, "failed to open stream"); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	return &QUICConn{
		Stream:  stream,
		Conn:    conn,
		reqAddr: conn.RemoteAddr(),
	}, nil
}

// QUICConn adapts a QUIC stream to net.Conn
type QUICConn struct {
	*quic.Stream
	*quic.Conn
	reqAddr net.Addr
}

func (c *QUICConn) Read(b []byte) (n int, err error) {
	return c.Stream.Read(b)
}

func (c *QUICConn) Write(b []byte) (n int, err error) {
	return c.Stream.Write(b)
}

func (c *QUICConn) Close() error {
	// Closing the stream closes the stream.
	// We also close the connection here because we are mapping 1 conn to 1 stream for simplicity in this adapter.
	// Hashicorp Raft maintains persistent connections, so closing usually means "disconnect peer".
	if err := c.Stream.Close(); err != nil {
		return err
	}
	return c.CloseWithError(0, "closed")
}

func (c *QUICConn) LocalAddr() net.Addr {
	return c.Conn.LocalAddr()
}

func (c *QUICConn) RemoteAddr() net.Addr {
	return c.reqAddr
}

func (c *QUICConn) SetDeadline(t time.Time) error {
	return c.Stream.SetDeadline(t)
}

func (c *QUICConn) SetReadDeadline(t time.Time) error {
	return c.Stream.SetReadDeadline(t)
}

func (c *QUICConn) SetWriteDeadline(t time.Time) error {
	return c.Stream.SetWriteDeadline(t)
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
