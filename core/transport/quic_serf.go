package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
	"github.com/hashicorp/memberlist"
	"github.com/quic-go/quic-go"
)

// QUICSerfTransport implements memberlist.Transport using QUIC.
// It uses QUIC Datagrams for packet-based gossip and QUIC Streams for
// reliable sync. It does not own a listener: inbound connections
// arrive from the shared control-plane listener's ALPN router
// (GOBLIN-DIV-023); only the dial side opens connections, offering
// exactly the serf-quic ALPN.
type QUICSerfTransport struct {
	tlsConf   *tls.Config
	advertise *net.UDPAddr

	packetCh chan *memberlist.Packet
	streamCh chan net.Conn

	// Cache active connections to peers for sending datagrams
	connsMu sync.Mutex
	conns   map[string]*quic.Conn

	ctx    context.Context
	cancel context.CancelFunc
}

// NewRoutedQUICSerfTransport builds the serf transport over routed
// connections. advertise is the node's single advertised control-plane
// address - it becomes the serf member address, so it must be dialable
// by peers.
func NewRoutedQUICSerfTransport(routed <-chan *quic.Conn, advertise net.Addr, tlsConf *tls.Config) (*QUICSerfTransport, error) {
	adv, ok := advertise.(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("advertise address %v is not a UDP address", advertise)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &QUICSerfTransport{
		tlsConf:   tlsConf.Clone(),
		advertise: adv,
		packetCh:  make(chan *memberlist.Packet, 64),
		streamCh:  make(chan net.Conn, 64),
		conns:     make(map[string]*quic.Conn),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Consume connections handed over by the ALPN router.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn, chOK := <-routed:
				if !chOK {
					return
				}
				go t.handleConn(conn)
			}
		}
	}()

	return t, nil
}

// FinalAdvertiseAddr returns the address to advertise: the node's
// single control-plane address unless memberlist overrides it.
func (t *QUICSerfTransport) FinalAdvertiseAddr(ip string, port int) (net.IP, int, error) {
	if ip == "" || ip == "0.0.0.0" {
		return t.advertise.IP, t.advertise.Port, nil
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return nil, 0, fmt.Errorf("invalid ip: %s", ip)
	}
	if port == 0 {
		port = t.advertise.Port
	}
	return addr, port, nil
}

// WriteTo sends a packet to the given address (connectionless semantics).
// In QUIC, we use Datagrams on an established connection.
func (t *QUICSerfTransport) WriteTo(b []byte, addr string) (time.Time, error) {
	// 1. Get or Dial Connection
	conn, err := t.getOrDial(addr)
	if err != nil {
		return time.Now(), err
	}

	// 2. Send Datagram
	// SendDatagram is non-blocking usually, but might error if too large or queue full.
	err = conn.SendDatagram(b)
	return time.Now(), err
}

// PacketCh returns a channel for reading incoming packets.
func (t *QUICSerfTransport) PacketCh() <-chan *memberlist.Packet {
	return t.packetCh
}

// DialTimeout creates a reliable stream connection to the given address.
func (t *QUICSerfTransport) DialTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	// Re-use connection or dial
	// Memberlist expects a net.Conn. We give a stream adapted as net.Conn.
	conn, err := t.getOrDial(addr)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}

	return &QUICConn{
		Stream:  stream,
		Conn:    conn,
		reqAddr: conn.RemoteAddr(),
	}, nil
}

// StreamCh returns a channel for accepting incoming stream connections.
func (t *QUICSerfTransport) StreamCh() <-chan net.Conn {
	return t.streamCh
}

// Shutdown closes the transport's dialed connections. The shared
// listener stays open - it belongs to the supervisor, not this adapter.
func (t *QUICSerfTransport) Shutdown() error {
	t.cancel()

	var err error
	t.connsMu.Lock()
	defer t.connsMu.Unlock()
	for _, conn := range t.conns {
		err = errors.Join(err, conn.CloseWithError(0, "shutdown"))
	}
	t.conns = nil

	return err
}

// getOrDial retrieves an existing connection or dials a new one.
func (t *QUICSerfTransport) getOrDial(addr string) (*quic.Conn, error) {
	t.connsMu.Lock()
	conn, ok := t.conns[addr]
	t.connsMu.Unlock()

	if ok {
		// Check if closed
		select {
		case <-conn.Context().Done():
			// Closed, remove and redial
			t.connsMu.Lock()
			delete(t.conns, addr)
			t.connsMu.Unlock()
		default:
			return conn, nil
		}
	}

	// Dial
	// We use a short timeout for the handshake to avoid blocking WriteTo for too long
	ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
	defer cancel()

	clientTLS := t.tlsConf.Clone()
	if clientTLS == nil {
		clientTLS = &tls.Config{InsecureSkipVerify: true}
	}
	// The dial must offer exactly serf-quic: a multi-ALPN client offer
	// would let the shared listener negotiate some other plane.
	clientTLS.NextProtos = []string{ALPNSerfQUIC}

	quicConf := &quic.Config{
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  60 * time.Second,
		EnableDatagrams: true,
	}

	newConn, err := dialWithClusterNotReadyRetry(ctx, addr, clientTLS, quicConf)
	if err != nil {
		return nil, err
	}

	t.connsMu.Lock()
	// Double check
	if existing, exists := t.conns[addr]; exists {
		t.connsMu.Unlock()
		// The winning connection is unaffected by a failed close of the
		// race loser; the failure only risks leaking it, so log and go on.
		if cerr := newConn.CloseWithError(0, "race"); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close raced serf connection failed", logattr.Addr(addr), logattr.Err(cerr))
		}
		return existing, nil
	}
	t.conns[addr] = newConn
	t.connsMu.Unlock()

	// Start handling this outgoing connection's incoming packets/streams locally?
	// Actually, QUIC is bidirectional. If we dial Peer B, Peer B might send packets back on this connection.
	// So we should handle incoming on outgoing connections too?
	// Memberlist usually treats Dial as mostly one-way for sync, but WriteTo is UDP.
	// If I WriteTo(B), I get a connection. B might reply.
	// We should monitor this connection for incoming stuff too.
	go t.handleConn(newConn)

	return newConn, nil
}

// handleConn demultiplexes streams and datagrams from a connection.
func (t *QUICSerfTransport) handleConn(conn *quic.Conn) {
	// 1. Accept Streams
	go func() {
		for {
			stream, err := conn.AcceptStream(t.ctx)
			if err != nil {
				return
			}
			// Push to StreamCh
			t.streamCh <- &QUICConn{
				Stream:  stream,
				Conn:    conn,
				reqAddr: conn.RemoteAddr(),
			}
		}
	}()

	// 2. Receive Datagrams
	for {
		msg, err := conn.ReceiveDatagram(t.ctx)
		if err != nil {
			return
		}
		// Push to PacketCh
		t.packetCh <- &memberlist.Packet{
			Buf:       msg,
			From:      conn.RemoteAddr(),
			Timestamp: time.Now(),
		}
	}
}
