package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/hashicorp/raft"
	"github.com/quic-go/quic-go"
)

// QUICStreamLayer implements raft.StreamLayer over QUIC.
type QUICStreamLayer struct {
	listener *quic.Listener
	addr     net.Addr
	tlsConf  *tls.Config
}

// NewQUICStreamLayer creates a new QUIC stream layer.
func NewQUICStreamLayer(bindAddr string, tlsConf *tls.Config) (*QUICStreamLayer, error) {
	quicConf := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
	}

	ln, err := quic.ListenAddr(bindAddr, tlsConf, quicConf)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on QUIC addr %s: %w", bindAddr, err)
	}

	return &QUICStreamLayer{
		listener: ln,
		addr:     ln.Addr(),
		tlsConf:  tlsConf,
	}, nil
}

// Accept accepts the next connection.
func (l *QUICStreamLayer) Accept() (net.Conn, error) {
	ctx := context.Background()
	conn, err := l.listener.Accept(ctx)
	if err != nil {
		return nil, err
	}

	stream, err := conn.AcceptStream(ctx)
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

// Close closes the listener.
func (l *QUICStreamLayer) Close() error {
	return l.listener.Close()
}

// Addr returns the listener's address.
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
	// Important: for QUIC, NextProtos must match
	if len(clientTLS.NextProtos) == 0 {
		clientTLS.NextProtos = []string{"raft-quic"}
	}

	conn, err := quic.DialAddr(ctx, string(addr), clientTLS, quicConf)
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
