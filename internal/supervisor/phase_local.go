package supervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"google.golang.org/protobuf/types/known/anypb"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
	gapiproduct "github.com/goppydae/gapi/core/product"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/logattr"
)

// phaseLocal runs Phase 1: everything this node is on its own, before it
// is a cluster member. Node identity, transport credentials, the single
// control-plane listener, the node-scoped event bus, and the agent
// manager.
//
// Nothing here depends on Serf, Raft or the scheduler, which is why it
// can run - and now does run - before them. It used to be interleaved
// with Phase 4, so the local runtime was not available until after the
// cluster stack was built.
//
// On success st carries: nodeID, secretKey, tlsCfg, listen/advertise
// addresses, tags, sharedLn, localBus, lifecycleBus, agentMgr. The
// caller owns closing sharedLn.
// The sub-step order is load-bearing and is the pre-split order: the
// production TLS gates fail closed and must be the FIRST thing a
// misconfigured production node hears about. Resolving addresses first
// would report a missing port on a node whose real problem is that it
// has no certificate, which is what tls_gate_test.go pins.
func (s *Supervisor) phaseLocal(ctx context.Context, st *runState) error {
	if err := s.localIdentity(ctx, st); err != nil {
		return err
	}
	if err := s.buildTLS(ctx, st); err != nil {
		return err
	}
	if err := s.localAddressing(ctx, st); err != nil {
		return err
	}
	if err := s.bindControlPlane(ctx, st); err != nil {
		return err
	}
	return s.localRuntime(ctx, st)
}

// localIdentity resolves who this node is and its gossip secret.
func (s *Supervisor) localIdentity(ctx context.Context, st *runState) error {
	st.nodeID = s.cfg.NodeID
	if st.nodeID == "" {
		host, _ := os.Hostname()
		st.nodeID = host
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "goblin supervisor starting", logattr.NodeID(st.nodeID))
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "detected resources",
		slog.String("cpu_cores", s.cfg.Tags["cpu"]), slog.String("memory_bytes", s.cfg.Tags["memory"]))

	if s.cfg.EncryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(s.cfg.EncryptionKey)
		if err != nil {
			return fmt.Errorf("failed to decode encryption key: %w", err)
		}
		if len(key) != 32 && len(key) != 16 && len(key) != 24 {
			return fmt.Errorf("encryption key must be 16, 24, or 32 bytes (got %d)", len(key))
		}
		st.secretKey = key
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "serf encryption enabled")
	}
	return nil
}

// localAddressing resolves what this node binds and advertises, and
// mints the per-boot capability identity that rides the gossip tags.
func (s *Supervisor) localAddressing(ctx context.Context, st *runState) error {
	// Resolve the single control-plane address. The advertised form is
	// what peers dial for every protocol - member address, raft server
	// address, and RPC endpoint are all the same "ip:port".
	listenHost, listenPortStr, err := net.SplitHostPort(s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", s.cfg.ListenAddr, err)
	}
	listenPort, err := strconv.Atoi(listenPortStr)
	if err != nil {
		return fmt.Errorf("invalid listen port: %w", err)
	}
	st.listenHost, st.listenPort = listenHost, listenPort

	// Capability issuer: per-boot Ed25519 keypair; the public key rides
	// the serf tags so any node resolves key_id -> key from gossip.
	issuer, err := capability.NewIssuer(st.nodeID)
	if err != nil {
		return fmt.Errorf("init capability issuer: %w", err)
	}
	s.issuer = issuer
	s.revocations = capability.NewRevocations()

	// No per-protocol address tags: the member's advertised address IS
	// the whole control plane (single listener, ALPN-routed).
	st.tags = map[string]string{
		"schema_hash": "v1-proto-hash",
		"version":     "0.3.3",
		"cap_key":     issuer.KeyID() + ":" + base64.StdEncoding.EncodeToString(issuer.PublicKey()),
	}
	if s.cfg.BootstrapExpect > 1 {
		// Advertised so peers can agree on the seed set - and refuse to
		// seed at all if they disagree about its size.
		st.tags[TagBootstrapExpect] = strconv.Itoa(s.cfg.BootstrapExpect)
	}
	for k, v := range s.cfg.Tags {
		st.tags[k] = v
	}

	advAddr := s.cfg.AdvertiseAddr
	if advAddr != "" {
		if ips, lerr := net.LookupIP(advAddr); lerr == nil && len(ips) > 0 {
			resolved := false
			for _, ip := range ips {
				if ip4 := ip.To4(); ip4 != nil {
					advAddr = ip4.String()
					resolved = true
					break
				}
			}
			if !resolved {
				advAddr = ips[0].String()
			}
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "resolved advertise address", logattr.From(s.cfg.AdvertiseAddr), logattr.To(advAddr))
		}
	}
	advPort := s.cfg.AdvertisePort
	if advPort == 0 {
		advPort = listenPort
	}
	advHost := advAddr
	if advHost == "" {
		advHost = listenHost
	}
	advertiseUDP, err := net.ResolveUDPAddr("udp", net.JoinHostPort(advHost, strconv.Itoa(advPort)))
	if err != nil {
		return fmt.Errorf("resolve advertise address: %w", err)
	}
	st.advertiseUDP = advertiseUDP
	return nil
}

// bindControlPlane opens the single control-plane listener. No plane is
// registered on it here; each phase registers the ALPNs it serves.
func (s *Supervisor) bindControlPlane(ctx context.Context, st *runState) error {
	// The single control-plane listener (GOBLIN-DIV-023). When mTLS is
	// configured, the CLI planes (gapi-quic, goblin-rpc) keep their
	// pre-collapse posture - server TLS without client certificates -
	// while the peer planes (serf-quic, raft-quic) stay mutually
	// authenticated; ALPN in the ClientHello selects the policy.
	lnTLS := st.tlsCfg.Clone()
	if lnTLS.ClientAuth == tls.RequireAndVerifyClientCert {
		cliTLS := lnTLS.Clone()
		cliTLS.ClientAuth = tls.NoClientCert
		cliTLS.ClientCAs = nil
		cliTLS.NextProtos = transport.RegistryALPNs()
		peerTLS := lnTLS.Clone()
		peerTLS.NextProtos = transport.RegistryALPNs()
		lnTLS.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			for _, p := range hello.SupportedProtos {
				if p == transport.ALPNGoblinRPC || p == transport.ALPNGapiQUIC {
					return cliTLS, nil
				}
			}
			return peerTLS, nil
		}
	}
	// The predicate, not a flag the listener owns: the accept loop starts
	// here in Phase 1 and must answer "not yet" for every cluster ALPN
	// until phaseCluster sets this (GOBLIN-DIV-051).
	sharedLn, err := transport.NewSharedListener(s.cfg.ListenAddr, lnTLS, st.planesUp.Load)
	if err != nil {
		return fmt.Errorf("bind control-plane listener: %w", err)
	}
	st.sharedLn = sharedLn
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "control-plane listener bound",
		logattr.Addr(sharedLn.Addr().String()), logattr.To(st.advertiseUDP.String()))
	return nil
}

// buildTLS produces the node's one tls.Config, shared by every plane.
// Production fails closed here rather than warning: an unverified
// listener must be an explicit dev-mode choice.
func (s *Supervisor) buildTLS(ctx context.Context, st *runState) error {
	if s.cfg.CertFile == "" || s.cfg.KeyFile == "" {
		if s.cfg.ProductionMode {
			return fmt.Errorf("production mode requires TLS: configure cert-file and key-file (and ca-file for mTLS)")
		}
		// Default to insecure: generate an ephemeral cert for QUIC,
		// which requires a certificate even when nothing verifies it.
		cert, err := generateDocsCert(st.nodeID)
		if err != nil {
			return fmt.Errorf("failed to generate ephemeral TLS cert: %w", err)
		}
		st.tlsCfg = &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
			NextProtos:         []string{transport.ALPNGapiQUIC, transport.ALPNGoblinRPC, transport.ALPNSerfQUIC},
		}
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "encryption disabled, using ephemeral self-signed cert")
		return nil
	}

	// CertManager gives the listener dynamic reloading.
	cm, err := NewCertManager(s.cfg.CertFile, s.cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("failed to load TLS certs: %w", err)
	}
	s.loops.spawn(tierRun, "cert-watch", func() { cm.Watch(ctx) })

	st.tlsCfg = &tls.Config{
		GetCertificate:       cm.GetCertificate,
		GetClientCertificate: cm.GetClientCertificate,
		MinVersion:           tls.VersionTLS12,
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "tls enabled with dynamic reloading")

	if s.cfg.CAFile == "" {
		// GOBLIN-DIV-043: without a CA there is no client certificate to
		// verify, so ClientAuth stays NoClientCert and the shared
		// listener admits any peer that can complete a handshake. On the
		// raft plane that peer can send InstallSnapshot, and FSM.Restore
		// installs the operator key registry straight from the payload
		// without passing through Apply - so an unauthenticated peer
		// could seed itself as the cluster's root of trust in one step.
		//
		// mTLS being AVAILABLE is not mTLS being REQUIRED, and this
		// branch was the difference: a warning that production could run
		// past.
		if s.cfg.ProductionMode {
			return fmt.Errorf("production mode requires mTLS: configure ca-file so raft peers " +
				"present verified client certificates (GOBLIN-DIV-043)")
		}
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "tls enabled but no ca provided, mtls disabled")
		return nil
	}

	caCert, err := os.ReadFile(s.cfg.CAFile)
	if err != nil {
		return fmt.Errorf("failed to read CA file: %w", err)
	}
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caCert)
	st.tlsCfg.ClientCAs = caPool
	st.tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	st.tlsCfg.RootCAs = caPool
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "raft mtls enabled")
	return nil
}

// localRuntime brings up the node-scoped event bus and the agent
// manager. The manager is stored on the Supervisor as well as on st
// because the Phase 0 reaper reads it to attribute exited children, and
// that loop starts before this one.
func (s *Supervisor) localRuntime(ctx context.Context, st *runState) error {
	// Node-scoped, not distributed: this bus never leaves the node.
	st.localBus = gapieventbus.NewInprocBus[*anypb.Any]()
	st.lifecycleBus = (*gapilifecycle.TypedBus)(st.localBus)

	// Composed through the kernel's own registry rather than spelled
	// here, so the name goblin READS cannot drift from the name the
	// embedded kernel would read for the same key (GOBLIN-DIV-055).
	pyRunnerPath := os.Getenv(gapiproduct.EnvKey("PY_RUNNER"))
	if pyRunnerPath == "" {
		pyRunnerPath = defaultPyRunner()
	}
	// Verification key for production-mode signed discovery (review R20):
	// config first, then env, mirroring the GAPI supervisor.
	verifyKeyPath := s.cfg.AgentVerifyKey
	if verifyKeyPath == "" {
		verifyKeyPath = os.Getenv(gapiproduct.EnvKey("VERIFY_KEY"))
	}
	var verifyKey ed25519.PublicKey
	if verifyKeyPath != "" {
		pk, err := gapicrypto.LoadPublic(verifyKeyPath)
		if err != nil {
			return fmt.Errorf("failed to load agent verification key %q: %w", verifyKeyPath, err)
		}
		verifyKey = pk
	}

	st.agentMgr = gapiagentmgr.NewAgentManager(st.localBus, st.lifecycleBus, pyRunnerPath, s.cfg.ProductionMode, verifyKey)
	s.agentMgr = st.agentMgr

	// Discovery registers what it finds. Nothing STARTS a discovered
	// agent: the documented Phase 2 does not exist, so these are
	// scheduling templates only. See GOBLIN-DIV-050.
	discovered, err := st.agentMgr.DiscoverFromPaths()
	if err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent discovery warning", logattr.Err(err))
	} else {
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "local agent manager initialized", logattr.Count(len(discovered)))
	}
	return nil
}

// defaultPyRunner resolves the Python ADK runner when the environment
// does not name one.
//
// It was the literal "/usr/local/bin/gapi-runner", which was wrong twice
// over. It named the kernel to an operator installing Python agent
// support (GOBLIN-DIV-056), and NOTHING provisions that path - not the
// NixOS module, not the Magefile, not any document. A default nobody
// satisfies is a mechanism built and never wired, so replacing it cannot
// regress a working deployment: there were none resting on it.
//
// Resolution mirrors the kernel's own resolvePyRunner: beside the running
// binary first, then relative to the working directory, which is what a
// checkout looks like. Neither is guaranteed to exist, and that is
// deliberate - the agent manager reports a missing runner when a Python
// agent is actually started, which is the boundary where the failure is
// attributable to something an operator did.
func defaultPyRunner() string {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "adk", "python", "agent", "runner.py")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return filepath.Join("adk", "python", "agent", "runner.py")
}
