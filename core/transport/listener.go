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
	// CodeALPNNotServing: the negotiated ALPN is in the registry and
	// NOTHING IS EVER GOING TO SERVE IT here - a tombstoned row, or a
	// plane this build does not carry. Not retryable.
	CodeALPNNotServing = quic.ApplicationErrorCode(0x100)
	// CodeAdapterBacklogged: the adapter's accept channel is full; the
	// connection is refused rather than stalling every other plane.
	CodeAdapterBacklogged = quic.ApplicationErrorCode(0x101)
	// CodeClusterNotReady: a CLUSTER ALPN arrived before the cluster
	// stack registered it - Phase 4 has not completed on this node.
	// RETRYABLE, and that is the entire reason it is a separate code
	// (GOBLIN-DIV-051).
	//
	// It used to share 0x100 with the case above, and the conflation was
	// not cosmetic. A joining peer that cannot tell "not yet" from
	// "never" must treat a transient phase skew as a hard join failure,
	// which is exactly what it did: node-2 dialling node-1 during the
	// window before node-1 registered serf-quic got a refusal it read as
	// fatal, gave up, and the cluster never formed a leader. That race
	// was intermittent and its signature was a placement timeout, which
	// is how it survived as an unexplained flake across three suites.
	CodeClusterNotReady = quic.ApplicationErrorCode(0x102)
)

// registryALPNs is the listener's view of the ecosystem ALPN registry:
// the identifiers this listener may ever advertise or route. Adding a
// row is a registry change (ecosystem manifesto section 6).
var registryALPNs = []string{ALPNGapiQUIC, ALPNGoblinRPC, ALPNSerfQUIC, ALPNRaftQUIC, ALPNGoblinCkpt}

// clusterALPNs are the protocols that cannot exist before Phase 4,
// because the stack that serves them is built there. A refusal on one of
// these before the cluster is up means NOT YET; a refusal on any other
// ALPN means never.
//
// gapi-quic is deliberately absent even though its handler also
// registers late: serving it earlier is a redesign of what that handler
// depends on (the distributed bus and membership are Phase 4 objects),
// not a reordering, so promising "not yet" would promise something this
// node will not deliver by waiting.
var clusterALPNs = map[string]struct{}{
	ALPNGoblinRPC: {},
	ALPNSerfQUIC:  {},
	ALPNRaftQUIC:  {},
}

// IsClusterALPN reports whether alpn is served only once the cluster
// stack exists.
func IsClusterALPN(alpn string) bool {
	_, ok := clusterALPNs[alpn]
	return ok
}

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

	// clusterReady reports whether Phase 4 has completed on this node.
	// A PREDICATE supplied at construction rather than a flag set later:
	// the listener is built in Phase 1 and starts accepting immediately,
	// so a value assigned "shortly after" would be read before it exists,
	// and the wrong answer here is the silent one - a peer told "never"
	// during the window it should have been told "not yet".
	clusterReady func() bool

	mu     sync.Mutex
	routes map[string]chan *quic.Conn
}

// NewSharedListener binds the control-plane address and starts the
// accept loop. The TLS config is cloned; its NextProtos are replaced
// with the full ALPN registry (datagrams are enabled - serf gossip
// requires RFC 9221 support end to end).
// clusterReady is required, not optional. It reports whether the cluster
// stack has registered its planes; the listener uses it to tell a peer
// "not yet" instead of "never" (GOBLIN-DIV-051). A nil predicate is a
// programming error rather than a default, because the default that
// would suit a test - always ready - is the answer that reintroduces the
// defect in production.
func NewSharedListener(bindAddr string, tlsCfg *tls.Config, clusterReady func() bool) (*SharedListener, error) {
	if clusterReady == nil {
		return nil, fmt.Errorf("shared listener: clusterReady predicate is required " +
			"(GOBLIN-DIV-051: without it a cluster ALPN arriving before Phase 4 is refused as " +
			"permanently unserved and the peer gives up instead of retrying)")
	}
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
		ln:           ln,
		cancel:       cancel,
		clusterReady: clusterReady,
		routes:       make(map[string]chan *quic.Conn),
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
			// Phase-aware admission. The distinction is the whole point:
			// not-ready-yet is retryable and nothing-is-listening is not,
			// so a peer that cannot tell them apart must treat a
			// transient phase skew as a hard failure.
			if IsClusterALPN(alpn) && !l.clusterReady() {
				slog.Default().LogAttrs(ctx, slog.LevelInfo, "refusing connection: cluster not ready",
					slog.String("alpn", alpn), logattr.Addr(conn.RemoteAddr().String()))
				_ = conn.CloseWithError(CodeClusterNotReady, "cluster not ready: "+alpn+" is served from Phase 4")
				continue
			}
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
