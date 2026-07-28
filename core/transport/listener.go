package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
	"github.com/quic-go/quic-go"
)

// QUIC application error codes the shared listener closes with. Named,
// never reused: a refused connection tells the peer exactly why.
const (
	// CodeALPNNotServing: the negotiated ALPN is in the registry but no
	// adapter is attached yet (phase-aware admission: cluster protocols
	// before the cluster exists) or ever (tombstoned row).
	CodeALPNNotServing = quic.ApplicationErrorCode(0x100)
	// CodeAdapterBacklogged: the adapter's accept channel is full; the
	// connection is refused rather than stalling every other plane.
	CodeAdapterBacklogged = quic.ApplicationErrorCode(0x101)
)

// registryALPNs is the listener's view of the ecosystem ALPN registry:
// the identifiers this listener may ever advertise or route. Adding a
// row is a registry change (ecosystem manifesto section 6).
var registryALPNs = []string{ALPNGapiQUIC, ALPNGoblinRPC, ALPNSerfQUIC, ALPNRaftQUIC, ALPNGoblinCkpt}

// RegistryALPNs returns the full ALPN registry the shared listener
// advertises; per-ALPN TLS policies (GetConfigForClient) must carry
// the same list or negotiation would silently narrow.
func RegistryALPNs() []string {
	return append([]string(nil), registryALPNs...)
}

// SharedListener is goblind's single control-plane QUIC listener
// (GOBLIN-DIV-023): every protocol shares one bind address and is
// routed to its adapter by negotiated ALPN. Adapters attach with
// Register; a connection negotiating an ALPN with no attached adapter
// is refused at accept with CodeALPNNotServing - fail closed, which is
// also the phase-aware admission rule (cluster ALPNs are not served
// until the cluster stack registers them).
type SharedListener struct {
	ln     *quic.Listener
	cancel context.CancelFunc

	mu     sync.Mutex
	routes map[string]chan *quic.Conn
}

// NewSharedListener binds the control-plane address and starts the
// accept loop. The TLS config is cloned; its NextProtos are replaced
// with the full ALPN registry (datagrams are enabled - serf gossip
// requires RFC 9221 support end to end).
func NewSharedListener(bindAddr string, tlsCfg *tls.Config) (*SharedListener, error) {
	if tlsCfg == nil {
		return nil, fmt.Errorf("shared listener requires a TLS config")
	}
	cfg := tlsCfg.Clone()
	cfg.NextProtos = append([]string(nil), registryALPNs...)

	ln, err := quic.ListenAddr(bindAddr, cfg, &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 10 * time.Second,
		MaxIdleTimeout:  60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("bind shared listener on %s: %w", bindAddr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	l := &SharedListener{
		ln:     ln,
		cancel: cancel,
		routes: make(map[string]chan *quic.Conn),
	}
	go l.acceptLoop(ctx)
	return l, nil
}

// Register attaches an adapter to an ALPN and returns its accept
// channel. Registering an ALPN twice, or one outside the registry, is
// a wiring bug and errors.
func (l *SharedListener) Register(alpn string) (<-chan *quic.Conn, error) {
	known := false
	for _, id := range registryALPNs {
		if id == alpn {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("ALPN %q is not in the registry", alpn)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.routes[alpn]; dup {
		return nil, fmt.Errorf("ALPN %q already has an adapter registered", alpn)
	}
	ch := make(chan *quic.Conn, 16)
	l.routes[alpn] = ch
	return ch, nil
}

// Addr is the single advertised control-plane address.
func (l *SharedListener) Addr() net.Addr {
	return l.ln.Addr()
}

// Close stops the accept loop and closes the listener.
func (l *SharedListener) Close() error {
	l.cancel()
	return l.ln.Close()
}

// acceptLoop routes each accepted connection by its negotiated ALPN.
// The loop never blocks on a slow adapter: a full accept channel
// refuses the connection so one stalled plane cannot starve the rest.
func (l *SharedListener) acceptLoop(ctx context.Context) {
	for {
		conn, err := l.ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // shutdown
			}
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "shared listener accept failed", logattr.Err(err))
			continue
		}

		alpn := conn.ConnectionState().TLS.NegotiatedProtocol
		l.mu.Lock()
		route, serving := l.routes[alpn]
		l.mu.Unlock()

		if !serving {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "refusing connection: ALPN not serving",
				slog.String("alpn", alpn), logattr.Addr(conn.RemoteAddr().String()))
			_ = conn.CloseWithError(CodeALPNNotServing, "ALPN "+alpn+" not serving")
			continue
		}
		select {
		case route <- conn:
		default:
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "refusing connection: adapter backlogged",
				slog.String("alpn", alpn), logattr.Addr(conn.RemoteAddr().String()))
			_ = conn.CloseWithError(CodeAdapterBacklogged, "adapter for "+alpn+" backlogged")
		}
	}
}
