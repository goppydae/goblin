package consensus

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/raft"
)

// TLSStreamLayer implements raft.StreamLayer interface for TLS support
type TLSStreamLayer struct {
	addr     net.Addr
	listener net.Listener
	tlsCfg   *tls.Config
}

// NewTLSStreamLayer creates a new TLS stream layer
func NewTLSStreamLayer(bindAddr string, tlsCfg *tls.Config) (*TLSStreamLayer, error) {
	// Create a TCP listener first
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", bindAddr, err)
	}

	// Wrap in TLS
	tlsListener := tls.NewListener(listener, tlsCfg)

	// Resolve the address
	addr := listener.Addr()

	return &TLSStreamLayer{
		addr:     addr,
		listener: tlsListener,
		tlsCfg:   tlsCfg,
	}, nil
}

// Dial makes an outgoing connection (with TLS)
func (t *TLSStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout: timeout,
	}
	// Dial with TLS
	return tls.DialWithDialer(dialer, "tcp", string(address), t.tlsCfg)
}

// Accept accepts an incoming connection
func (t *TLSStreamLayer) Accept() (net.Conn, error) {
	return t.listener.Accept()
}

// Close closes the listener
func (t *TLSStreamLayer) Close() error {
	return t.listener.Close()
}

// Addr returns the listener address
func (t *TLSStreamLayer) Addr() net.Addr {
	return t.addr
}
