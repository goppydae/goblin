package supervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/serf/serf"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapiclock "github.com/goppydae/gapi/core/clock"
	gapiconfig "github.com/goppydae/gapi/core/config"
	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
	gapilogging "github.com/goppydae/gapi/core/logging"
	"github.com/goppydae/gapi/core/procsig"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"
	"github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/hlc"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds configuration for the Supervisor
type Config struct {
	NodeID string
	// ListenAddr is the single control-plane bind address: every
	// protocol (gapi-quic, goblin-rpc, serf-quic, raft-quic) shares it,
	// routed by ALPN (GOBLIN-DIV-023). Metrics is the only other port.
	ListenAddr    string
	AdvertiseAddr string
	AdvertisePort int
	RaftDir       string
	// RaftSnapshotThreshold overrides how many outstanding Raft log
	// entries trigger a compaction snapshot (raft.DefaultConfig: 8192).
	// 0 keeps the Raft default; operators tune it down on write-heavy
	// clusters to bound the trailing log's disk footprint and how much a
	// late-joining node must replay before it is caught up.
	RaftSnapshotThreshold uint64
	// RaftSnapshotInterval overrides how often Raft checks whether a
	// compaction snapshot is due (raft.DefaultConfig: 120s). 0 keeps the
	// Raft default.
	RaftSnapshotInterval time.Duration
	// RaftTrailingLogs overrides how many log entries Raft retains after
	// a snapshot for fast follower replay instead of a full snapshot
	// transfer (raft.DefaultConfig: 10240). 0 keeps the Raft default.
	RaftTrailingLogs uint64
	JoinAddr         string
	// BootstrapExpect is the number of seed nodes that must be visible
	// through gossip before the cluster seeds itself. Every seed is
	// configured with the same number and they elect one bootstrapper
	// among themselves, so no node has to be designated by hand. 0 (or
	// 1) keeps the seed model: whichever node has no JoinAddr
	// bootstraps alone.
	BootstrapExpect int
	Tags            map[string]string
	EncryptionKey   string // Base64 encoded 32-byte key
	CertFile        string
	KeyFile         string
	CAFile          string
	MetricsAddr     string
	// ProductionMode restricts embedded-GAPI agent discovery to binaries
	// with verified signatures (review R20).
	ProductionMode bool
	// AgentVerifyKey is the path to the Ed25519 public key that verifies
	// agent-binary signatures; falls back to $RUNTIME_VERIFY_KEY.
	AgentVerifyKey string
	// OperatorKeyFiles are paths to hex-encoded Ed25519 public keys that
	// bootstrap the cluster's operator registry (GOBLIN-DIV-015 piece 1).
	// They are this node's claim about the root of trust; they become
	// authoritative only once committed to Raft. Empty means the cluster
	// refuses every mutating verb.
	OperatorKeyFiles []string
	// Logging mirrors gapi's logging configuration (level, format, file
	// rotation, Loki); handlers are built by the kernel's core/logging.
	Logging gapiconfig.LoggingConfig
	// NetworkGateTimeout bounds the network-readiness phase gate: when
	// nonzero, Run blocks until the kernel's agent.network.running topic
	// fires on the local bus, failing loudly on expiry (GOBLIN-DIV-011,
	// R13). Zero disables the gate - the topic has no producer unless a
	// network agent is deployed.
	NetworkGateTimeout time.Duration
	// Pid1Mode activates the embedded kernel's Phase 0 pre-userspace
	// boot before any cluster code, and the reversed teardown on
	// shutdown (goblin-architecture.md). goblind IS the init process.
	Pid1Mode         bool
	NoEarlyMounts    bool
	WatchdogDevice   string
	WatchdogInterval time.Duration
	ShutdownGrace    time.Duration
}

// Supervisor manages the Goblin daemon components
type Supervisor struct {
	cfg      Config
	agentMgr *gapiagentmgr.AgentManager // GAPI agent manager (optional)

	// issuer mints capability tokens under this boot's keypair;
	// revocations is the gossip-merged revocation Bloom filter.
	issuer      *capability.Issuer
	revocations *capability.Revocations
}

// New creates a new Supervisor
func New(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Run starts the supervisor and blocks until context is canceled
func (s *Supervisor) Run(ctx context.Context) (err error) {
	// Logging first: everything after this logs through slog.
	logger, logCloser, err := gapilogging.Build(&s.cfg.Logging)
	if err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func() {
		if cerr := logCloser.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close log sink: %w", cerr)
		}
	}()
	slog.SetDefault(logger)

	// Phase 0 (PID-1 mode): the embedded kernel's pre-userspace
	// obligations run before any cluster code - a hung network layer
	// can never block init duties (goblin-architecture.md boot phases).
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	// Why the context was cancelled, so a fatal misconfiguration exits
	// nonzero instead of looking like a clean stop. Buffered and
	// select-guarded so the first cause wins and no failure path can
	// block on it.
	fatalCh := make(chan error, 1)
	failFatal := func(cause error) {
		select {
		case fatalCh <- cause:
		default:
		}
		runCancel()
	}
	// The fatal cause must beat whatever return path actually fired.
	// Several returns between here and the bottom of Run are ctx-aware
	// and will surface "context canceled" the moment failFatal cancels,
	// which would report the symptom instead of the cause. Registered
	// first, so it runs last.
	defer func() {
		select {
		case ferr := <-fatalCh:
			err = ferr
		default:
		}
	}()
	var pid1 *pid1Completion
	if s.cfg.Pid1Mode {
		p, perr := s.enablePid1(runCtx, runCancel)
		if perr != nil {
			return fmt.Errorf("phase 0: %w", perr)
		}
		pid1 = p
	}
	ctx = runCtx

	nodeID := s.cfg.NodeID
	if nodeID == "" {
		host, _ := os.Hostname()
		nodeID = host
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "goblin supervisor starting", logattr.NodeID(nodeID))
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "detected resources",
		slog.String("cpu_cores", s.cfg.Tags["cpu"]), slog.String("memory_bytes", s.cfg.Tags["memory"]))

	// Decode Encryption Key
	var secretKey []byte
	if s.cfg.EncryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(s.cfg.EncryptionKey)
		if err != nil {
			return fmt.Errorf("failed to decode encryption key: %w", err)
		}
		if len(key) != 32 && len(key) != 16 && len(key) != 24 {
			return fmt.Errorf("encryption key must be 16, 24, or 32 bytes (got %d)", len(key))
		}
		secretKey = key
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "serf encryption enabled")
	}

	// Load TLS Config
	var tlsCfg *tls.Config
	if s.cfg.CertFile != "" && s.cfg.KeyFile != "" {
		// Use CertManager for dynamic reloading
		cm, err := NewCertManager(s.cfg.CertFile, s.cfg.KeyFile)
		if err != nil {
			return fmt.Errorf("failed to load TLS certs: %w", err)
		}
		go cm.Watch(ctx)

		tlsCfg = &tls.Config{
			GetCertificate:       cm.GetCertificate,
			GetClientCertificate: cm.GetClientCertificate,
			MinVersion:           tls.VersionTLS12,
		}
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "tls enabled with dynamic reloading")

		// If CA provided, enable Client Auth for mTLS
		if s.cfg.CAFile != "" {
			caCert, err := os.ReadFile(s.cfg.CAFile)
			if err != nil {
				return fmt.Errorf("failed to read CA file: %w", err)
			}
			caPool := x509.NewCertPool()
			caPool.AppendCertsFromPEM(caCert)
			tlsCfg.ClientCAs = caPool
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
			tlsCfg.RootCAs = caPool
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "raft mtls enabled")
		} else {
			// GOBLIN-DIV-043: without a CA there is no client
			// certificate to verify, so ClientAuth stays NoClientCert
			// and the shared listener admits any peer that can complete
			// a handshake. On the raft plane that peer can send
			// InstallSnapshot, and FSM.Restore installs the operator key
			// registry straight from the payload without passing through
			// Apply - so an unauthenticated peer could seed itself as the
			// cluster's root of trust in one step.
			//
			// mTLS being AVAILABLE is not mTLS being REQUIRED, and this
			// branch was the difference: a warning that production could
			// run past.
			if s.cfg.ProductionMode {
				return fmt.Errorf("production mode requires mTLS: configure ca-file so raft peers " +
					"present verified client certificates (GOBLIN-DIV-043)")
			}
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "tls enabled but no ca provided, mtls disabled")
		}
	} else {
		// Fail closed in production: an unverified ephemeral-cert listener
		// must be an explicit dev-mode choice, never a silent default.
		if s.cfg.ProductionMode {
			return fmt.Errorf("production mode requires TLS: configure cert-file and key-file (and ca-file for mTLS)")
		}
		// Default to insecure: Generate ephemeral cert for QUIC
		// QUIC requires a certificate even if we don't verify it (TLS 1.3)
		var err error
		cert, err := generateDocsCert(nodeID)
		if err != nil {
			return fmt.Errorf("failed to generate ephemeral TLS cert: %w", err)
		}
		tlsCfg = &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
			NextProtos:         []string{transport.ALPNGapiQUIC, transport.ALPNGoblinRPC, transport.ALPNSerfQUIC},
		}
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "encryption disabled, using ephemeral self-signed cert")
	}

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

	// Capability issuer: per-boot Ed25519 keypair; the public key rides
	// the serf tags so any node resolves key_id -> key from gossip.
	issuer, err := capability.NewIssuer(nodeID)
	if err != nil {
		return fmt.Errorf("init capability issuer: %w", err)
	}
	s.issuer = issuer
	s.revocations = capability.NewRevocations()

	// No per-protocol address tags: the member's advertised address IS
	// the whole control plane (single listener, ALPN-routed).
	tags := map[string]string{
		"schema_hash": "v1-proto-hash",
		"version":     "0.3.3",
		"cap_key":     issuer.KeyID() + ":" + base64.StdEncoding.EncodeToString(issuer.PublicKey()),
	}
	if s.cfg.BootstrapExpect > 1 {
		// Advertised so peers can agree on the seed set - and refuse to
		// seed at all if they disagree about its size.
		tags[TagBootstrapExpect] = strconv.Itoa(s.cfg.BootstrapExpect)
	}
	for k, v := range s.cfg.Tags {
		tags[k] = v
	}
	advAddr := s.cfg.AdvertiseAddr
	if advAddr != "" {
		if ips, err := net.LookupIP(advAddr); err == nil && len(ips) > 0 {
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

	// The single control-plane listener (GOBLIN-DIV-023). When mTLS is
	// configured, the CLI planes (gapi-quic, goblin-rpc) keep their
	// pre-collapse posture - server TLS without client certificates -
	// while the peer planes (serf-quic, raft-quic) stay mutually
	// authenticated; ALPN in the ClientHello selects the policy.
	lnTLS := tlsCfg.Clone()
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
	sharedLn, err := transport.NewSharedListener(s.cfg.ListenAddr, lnTLS)
	if err != nil {
		return fmt.Errorf("bind control-plane listener: %w", err)
	}
	defer func() {
		if cerr := sharedLn.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close control-plane listener: %w", cerr)
		}
	}()
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "control-plane listener bound",
		logattr.Addr(sharedLn.Addr().String()), logattr.To(advertiseUDP.String()))

	// Serf rides the shared listener: routed serf-quic connections in,
	// per-peer dials out.
	serfConns, err := sharedLn.Register(transport.ALPNSerfQUIC)
	if err != nil {
		return fmt.Errorf("register serf plane: %w", err)
	}
	serfTransport, err := transport.NewRoutedQUICSerfTransport(serfConns, advertiseUDP, tlsCfg)
	if err != nil {
		return fmt.Errorf("failed to create QUIC serf transport: %w", err)
	}

	membership, err := cluster.NewMembership(nodeID, listenHost, listenPort, advertiseUDP.IP.String(), advertiseUDP.Port, tags, secretKey, serfTransport)
	if err != nil {
		err = fmt.Errorf("failed to create membership: %w", err)
		if serr := serfTransport.Shutdown(); serr != nil { // Cleanup on failure
			return errors.Join(err, serr)
		}
		return err
	}
	defer func() {
		if serr := membership.Shutdown(); serr != nil && err == nil {
			err = fmt.Errorf("shutdown membership: %w", serr)
		}
	}()

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "serf membership initialized", logattr.Addr(advertiseUDP.String()))

	// Join if requested
	if s.cfg.JoinAddr != "" {
		if err := membership.Join([]string{s.cfg.JoinAddr}); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to join cluster", logattr.Addr(s.cfg.JoinAddr), logattr.Err(err))
		} else {
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "joined cluster", logattr.Addr(s.cfg.JoinAddr))
		}
	}

	// Create Raft consensus over the shared listener's raft-quic plane.
	raftConns, err := sharedLn.Register(transport.ALPNRaftQUIC)
	if err != nil {
		return fmt.Errorf("register raft plane: %w", err)
	}
	raftStream := transport.NewRoutedQUICStreamLayer(raftConns, advertiseUDP, tlsCfg)
	// With bootstrap-expect the initial configuration is not knowable
	// at construction time - the engine comes up unseeded and is
	// bootstrapped once gossip shows the whole seed set.
	seedAlone := s.cfg.JoinAddr == "" && s.cfg.BootstrapExpect < 2
	consensus, err := consensus.NewConsensus(nodeID, s.cfg.RaftDir, raftStream, seedAlone,
		s.cfg.RaftSnapshotThreshold, s.cfg.RaftSnapshotInterval, s.cfg.RaftTrailingLogs)
	if err != nil {
		return fmt.Errorf("failed to create consensus: %w", err)
	}
	operatorKeys, err := loadOperatorKeys(s.cfg.OperatorKeyFiles)
	if err != nil {
		return fmt.Errorf("load operator keys: %w", err)
	}
	// GOBLIN-DIV-049: nothing else retires a migration record left by a
	// leader that died mid-move, and the reconciler now honours those
	// records - so without this, one dead coordinator makes an instance
	// permanently unrecoverable.
	go runMigrationSweeper(ctx, consensus, slog.Default(), time.Second)

	go func() {
		serr := runOperatorKeySeeder(ctx, consensus, operatorKeys, slog.Default(), time.Second)
		if serr == nil || errors.Is(serr, context.Canceled) {
			return // seeded, or shutting down
		}
		// Never fatal. See ErrOperatorConfigStale for why: the flag is
		// inert on an already-seeded cluster, and the fatal-versus-benign
		// distinction is not reliably knowable from here. The
		// goblin_operator_key_config_drift gauge is what an operator
		// alerts on; this line is for whoever is reading the log at the
		// time.
		slog.Default().LogAttrs(ctx, slog.LevelError,
			"some or all of this node's configured operator keys are absent from the cluster registry; those keys authorize nothing and re-supplying them via --operator-key is inert",
			logattr.Err(serr))
	}()
	if s.cfg.BootstrapExpect > 1 {
		// In the background: the node serves gossip and RPC while it
		// waits: it is running, the cluster simply is not seeded yet.
		// Blocking here would make an incomplete seed set look like a
		// dead daemon.
		go func() {
			if berr := runBootstrapExpect(ctx, s.cfg.BootstrapExpect, s.cfg.JoinAddr, membership, consensus); berr != nil {
				if errors.Is(berr, context.Canceled) {
					return // shutting down
				}
				// A disagreement about the seed set would split the
				// cluster; refuse to run rather than seed half of it.
				slog.Default().LogAttrs(ctx, slog.LevelError, "bootstrap-expect failed", logattr.Err(berr))
				failFatal(berr)
			}
		}()
	}
	defer func() {
		if serr := consensus.Shutdown(); serr != nil && err == nil {
			err = fmt.Errorf("shutdown consensus: %w", serr)
		}
	}()

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "raft consensus initialized", logattr.Addr(advertiseUDP.String()))

	// Create distributed event bus
	bus := eventbus.NewDistributedEventBus(nodeID, membership, consensus)

	// Bridge Serf events to EventBus
	membership.SetEventHandler(func(e serf.Event) {
		// The distributed bus ingests goblin.event user events here:
		// Serf supports a single handler, so the supervisor fans out.
		bus.HandleSerfEvent(e)
		switch ev := e.(type) {
		case serf.MemberEvent:
			for _, member := range ev.Members {
				var topic string
				switch ev.EventType() {
				case serf.EventMemberJoin:
					topic = "cluster.node.joined"
					// Every node races addVoterWithRetry for every join:
					// only the (eventual) leader's attempt lands, and the
					// rest expire quietly with ErrNotAdmitting. Gating on
					// instantaneous leadership here is the bug that left
					// three independent single-node rafts (R6 + 2b e2e).
					if member.Name != nodeID {
						// Single-listener model: the member's advertised
						// serf address IS its raft address (and its RPC
						// address) - no per-protocol tags exist.
						peerAddr := net.JoinHostPort(member.Addr.String(), strconv.Itoa(int(member.Port)))
						slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "adding raft voter", logattr.Member(member.Name), logattr.Addr(peerAddr))
						go func(name, addr string) {
							err := addVoterWithRetry(context.Background(), consensus, name, addr)
							switch {
							case err == nil:
							case errors.Is(err, ErrNotAdmitting):
								slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "voter admission left to the leader", logattr.Member(name))
							default:
								slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to add voter", logattr.Member(name), logattr.Err(err))
							}
						}(member.Name, peerAddr)
					}
				case serf.EventMemberLeave:
					topic = "cluster.node.left"
				case serf.EventMemberFailed:
					topic = "cluster.node.failed"
				case serf.EventMemberUpdate:
					topic = "cluster.node.updated"
				case serf.EventMemberReap:
					topic = "cluster.node.reaped"
				}

				if topic != "" {
					payload := map[string]interface{}{
						"name":   member.Name,
						"addr":   member.Addr.String(),
						"port":   member.Port,
						"status": member.Status.String(),
						"tags":   member.Tags,
					}
					// Serf event handler is a callback terminus: there is
					// no caller to propagate to, so a publish failure is
					// logged.
					if err := bus.PublishLocal("system", topic, payload, []string{"cluster", member.Name}); err != nil {
						slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "publish failed", logattr.Topic(topic), logattr.Member(member.Name), logattr.Err(err))
					}
				}
			}
		}
	})

	// Create Store
	kvStore := store.NewStore(consensus, bus)
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "distributed kv store initialized")

	// Create Scheduler
	sched := scheduler.NewScheduler(kvStore, membership, bus, consensus.IsLeader, func(addr string) (scheduler.RPCClient, error) {
		// addr is the target member's single advertised address; the
		// dial's goblin-rpc ALPN selects the plane.
		return NewQUICRPCClient(addr, tlsCfg)
	})
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler initialized")

	// Initialize local agent manager (always enabled)
	var agentMgr *gapiagentmgr.AgentManager

	// Create local event bus for GAPI (node-scoped, not distributed)
	localBus := gapieventbus.NewInprocBus[*anypb.Any]()

	// Create lifecycle event bus (aliased type)
	lifecycleBus := (*gapilifecycle.TypedBus)(localBus)

	// Initialize agent manager with standard Python runner
	pyRunnerPath := os.Getenv("RUNTIME_PY_RUNNER")
	if pyRunnerPath == "" {
		pyRunnerPath = "/usr/local/bin/gapi-runner"
	}
	// Verification key for production-mode signed discovery (review R20):
	// config first, then env, mirroring the GAPI supervisor.
	verifyKeyPath := s.cfg.AgentVerifyKey
	if verifyKeyPath == "" {
		verifyKeyPath = os.Getenv("RUNTIME_VERIFY_KEY")
	}
	var verifyKey ed25519.PublicKey
	if verifyKeyPath != "" {
		pk, err := gapicrypto.LoadPublic(verifyKeyPath)
		if err != nil {
			return fmt.Errorf("failed to load agent verification key %q: %w", verifyKeyPath, err)
		}
		verifyKey = pk
	}

	agentMgr = gapiagentmgr.NewAgentManager(localBus, lifecycleBus, pyRunnerPath, s.cfg.ProductionMode, verifyKey)

	// Discover agents using GAPI's standard search paths
	discovered, err := agentMgr.DiscoverFromPaths()
	if err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent discovery warning", logattr.Err(err))
	} else {
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "local agent manager initialized", logattr.Count(len(discovered)))
	}

	// Network-readiness phase gate (GOBLIN-DIV-011, R13): gate on the
	// kernel's exported topic constant, never a literal, with a hard
	// bound - PID 1 hanging forever is not an acceptable outcome.
	if s.cfg.NetworkGateTimeout > 0 {
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "waiting for network agent",
			logattr.Topic(gapieventbus.TopicAgentNetworkRunning),
			slog.Duration("timeout", s.cfg.NetworkGateTimeout))
		if gerr := localBus.WaitForTopic(ctx, "system", "", gapieventbus.TopicAgentNetworkRunning, s.cfg.NetworkGateTimeout, gapiclock.RealClock{}); gerr != nil {
			return fmt.Errorf("network-readiness gate: %w (no %s event within %s)",
				gerr, gapieventbus.TopicAgentNetworkRunning, s.cfg.NetworkGateTimeout)
		}
	}

	s.agentMgr = agentMgr

	// Register Scheduler RPC for CLI operations
	// Migration collaborators. The dialer is the same one the scheduler
	// uses - one place decides how a node is reached - and the resolver
	// is membership's view, so the coordinator cannot disagree with the
	// reconciler about where a node lives.
	migrateNodes := migration.NewRPCNodes(
		func(addr string) (migration.Caller, error) {
			return NewQUICRPCClient(addr, tlsCfg)
		},
		sched.NodeAddress,
	)

	schedulerRPC := &SchedulerRPC{
		scheduler:   sched,
		membership:  membership,
		consensus:   consensus,
		agentMgr:    agentMgr, // Wire agent manager to RPC
		issuer:      s.issuer,
		revocations: s.revocations,
		members:     membership,

		migrateNodes:  migrateNodes,
		migrateRaft:   migration.NewRaftProposer(consensus, 0),
		migrateLogger: slog.Default(),
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler rpc created")

	// Subscribe to cluster events (After RPC init so we can log to history)
	clusterEvents := []string{
		"cluster.node.joined",
		"cluster.node.left",
		"cluster.node.failed",
		"cluster.node.updated",
		"job.submitted",
		"job.assigned",
		"job.migrated",
	}

	for _, topic := range clusterEvents {
		bus.Subscribe(topic, func(e eventbus.Event) {
			var msg string
			if strings.HasPrefix(e.Topic, "cluster.node.") {
				action := strings.TrimPrefix(e.Topic, "cluster.node.")
				name, _ := e.Payload["name"].(string)
				msg = fmt.Sprintf("Node %s: %s", action, name)
			} else if strings.HasPrefix(e.Topic, "job.") {
				action := strings.TrimPrefix(e.Topic, "job.")
				jobID, _ := e.Payload["job_id"].(string)
				msg = fmt.Sprintf("Job %s: %s", action, jobID)
				if nodeID, ok := e.Payload["node_id"].(string); ok {
					msg = fmt.Sprintf("%s (on %s)", msg, nodeID)
				}
			}

			if msg != "" {
				slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "cluster event", logattr.Message(msg))
				schedulerRPC.AddEvent(msg)
			}
		})
	}

	bus.Subscribe("global.alert", func(e eventbus.Event) {
		msg := fmt.Sprintf("Alert: %v", e.Payload)
		slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "alert event", logattr.Message(msg))
		schedulerRPC.AddEvent(msg)
	})

	// Create QUIC RPC Handler
	quicServer := NewQUICRPCServer()

	// Register all scheduler handlers
	RegisterSchedulerHandlers(quicServer, schedulerRPC)

	// Register Node handlers
	instTracker := newInstanceTracker()
	// Checkpoint images live beside the Raft data rather than under a
	// separate root: both are node-local durable state with the same
	// lifetime, and an operator who relocates one means to relocate the
	// other. Sibling, not nested - wiping raft state must not discard
	// images that a migration in flight still needs.
	imageRoot := filepath.Join(filepath.Dir(s.cfg.RaftDir), "checkpoints")
	// Client TLS for dialing a peer's goblin-ckpt listener.
	//
	// This is the node's OWN tls.Config, not one synthesized from
	// CAFile. The two-node test proved why: building it from CAFile
	// alone produced a config that verified against the system roots,
	// so every dial failed with "certificate signed by unknown
	// authority" against the cluster's self-signed certs, while every
	// other plane worked because they all share tlsCfg. One config, one
	// verification policy, for every plane on the shared listener.
	//
	// DialAndFetch clones it before stamping the ALPN, so this stays
	// usable by the other planes.
	nodeRPC := &NodeRPC{
		agentMgr:  agentMgr,
		tracker:   instTracker,
		images:    migration.NewStore(imageRoot),
		ckptTLS:   tlsCfg,
		consensus: consensus,
	}
	RegisterNodeHandlers(quicServer, nodeRPC)

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler and node rpc handlers registered")

	// Local lifecycle status updates the tracker: an instance whose
	// process dies publishes FAILED/STOPPED on the local bus, and the
	// next heartbeat reports it so the leader can re-place (phase 2b).
	if err := localBus.Subscribe("system", "", gapieventbus.TopicAgentLifecycleStatus, func(e gapieventbus.Event[*anypb.Any]) {
		var st protopkg.LifecycleStatus
		if e.Payload == nil || e.Payload.UnmarshalTo(&st) != nil {
			return
		}
		slog.Default().LogAttrs(ctx, slog.LevelDebug, "local lifecycle status",
			logattr.InstanceID(st.AgentId), slog.String("state", st.State), slog.String("message", st.Message))
		switch st.State {
		case "RUNNING":
			instTracker.SetIfTracked(st.AgentId, "running")
		case "FAILED", "STOPPED":
			// A deliberate stop removes the instance from the tracker
			// before the event arrives; anything still tracked died.
			instTracker.SetIfTracked(st.AgentId, "failed")
		}
	}); err != nil {
		return fmt.Errorf("subscribe instance status: %w", err)
	}

	// Heartbeat publisher: this node's instance states, cluster-wide,
	// every cadence. The kernel bus is the transport; cadence and topic
	// are orchestrator policy (scheduler.HeartbeatCadence). The
	// heartbeat doubles as the locator update path (DDR-3/4/5): a
	// running instance's pid, start epoch, and pid-namespace inode ride
	// along, stamped by this node's HLC for last-writer-wins.
	hlcClock := hlc.New(nodeID)
	go func() {
		ticker := time.NewTicker(scheduler.HeartbeatCadence)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for id, info := range instTracker.Snapshot() {
					stamp := hlcClock.Now()
					payload := map[string]interface{}{
						"instance_id": id,
						"node_id":     nodeID,
						"state":       info.State,
						"hlc_wall":    stamp.Wall,
						"hlc_counter": stamp.Counter,
						"hlc_node":    stamp.Node,
					}
					if info.Pid > 0 {
						if pi, err := procsig.Identify(info.Pid); err == nil {
							payload["node_pid"] = pi.Pid
							payload["start_epoch"] = pi.StartEpoch
							payload["pid_ns_inode"] = pi.PidNsInode
						}
					}
					if err := bus.PublishCluster("system", "instance.heartbeat", payload, nil); err != nil {
						slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish instance heartbeat failed", logattr.InstanceID(id), logattr.Err(err))
					}
				}
			}
		}
	}()

	// Leader side: heartbeats feed the reconciler's health view and the
	// locator map; the HLC merges every remote stamp it sees.
	bus.Subscribe("instance.heartbeat", func(e eventbus.Event) {
		id, _ := e.Payload["instance_id"].(string)
		node, _ := e.Payload["node_id"].(string)
		state, _ := e.Payload["state"].(string)
		if id == "" {
			return
		}
		sched.ObserveHeartbeat(id, node, state, time.Now())

		stamp := hlc.Timestamp{
			Wall:    payloadInt64(e.Payload["hlc_wall"]),
			Counter: payloadUint32(e.Payload["hlc_counter"]),
			Node:    payloadString(e.Payload["hlc_node"]),
		}
		if stamp.IsZero() {
			return
		}
		hlcClock.Observe(stamp)
		if pid := payloadInt64(e.Payload["node_pid"]); pid > 0 {
			sched.ObserveLocator(id, scheduler.Locator{
				NodeID:     node,
				Pid:        int(pid),
				StartEpoch: payloadUint64(e.Payload["start_epoch"]),
				PidNsInode: payloadUint64(e.Payload["pid_ns_inode"]),
				At:         stamp,
			})
		}
	})

	// Revocation gossip: a revoking node broadcasts its filter; every
	// node merges what it sees (the filter only grows, so merge order
	// is irrelevant). Bytes ride the bus JSON as base64.
	bus.Subscribe("capability.revocation", func(e eventbus.Event) {
		var raw []byte
		switch v := e.Payload["filter"].(type) {
		case []byte:
			raw = v
		case string:
			decoded, derr := base64.StdEncoding.DecodeString(v)
			if derr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelWarn, "undecodable revocation snapshot", logattr.Err(derr))
				return
			}
			raw = decoded
		default:
			return
		}
		if err := s.revocations.Ingest(raw); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "reject revocation snapshot", logattr.Err(err))
		}
	})

	// The reconcile loop itself: leader-gated inside, kickable via
	// RegisterGlobalAgent/ScaleAgent for sub-interval placement latency.
	go sched.RunReconciler(ctx, 2*time.Second)

	// RPC and embedded-kernel planes ride the shared listener: the ALPN
	// router hands each plane its own connection stream.
	rpcConns, err := sharedLn.Register(transport.ALPNGoblinRPC)
	if err != nil {
		return fmt.Errorf("register rpc plane: %w", err)
	}
	gapiConns, err := sharedLn.Register(transport.ALPNGapiQUIC)
	if err != nil {
		return fmt.Errorf("register gapi plane: %w", err)
	}
	// Checkpoint image transfer. Registering the ALPN in the registry
	// only advertises it; without an adapter the router refuses the
	// connection with CodeALPNNotServing, which is how a migration
	// failed with "ALPN goblin-ckpt not serving" while every other
	// plane worked.
	ckptConns, err := sharedLn.Register(transport.ALPNGoblinCkpt)
	if err != nil {
		return fmt.Errorf("register checkpoint plane: %w", err)
	}
	go migration.NewServer(
		nodeRPC.Images(),
		checkpointAuthorizer(schedulerRPC.capabilityKeyResolver(), slog.Default()),
		slog.Default(),
	).Serve(ctx, ckptConns)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn, ok := <-rpcConns:
				if !ok {
					return
				}
				go quicServer.HandleConnection(conn)
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn, ok := <-gapiConns:
				if !ok {
					return
				}
				go handleQUICConn(conn, bus, membership)
			}
		}
	}()
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "rpc and gapi planes attached to control-plane listener")
	// Start Metrics Server
	if s.cfg.MetricsAddr != "" {
		go func() {
			http.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "metrics server listening", logattr.Addr(s.cfg.MetricsAddr))
			srv := &http.Server{
				Addr:              s.cfg.MetricsAddr,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelWarn, "metrics server failed", logattr.Err(err))
			}
		}()
	}

	// Start Cluster Monitor (stats & events)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastRaftState string
		var lastLeaderID string

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Update Raft Stats
				stats := consensus.Stats()
				if term, ok := stats["term"]; ok {
					val, _ := strconv.ParseFloat(term, 64)
					metrics.RaftTerm.Set(val)
				}

				state := stats["state"]

				// Log state changes
				if lastRaftState != "" && state != lastRaftState {
					schedulerRPC.AddEvent(fmt.Sprintf("Raft State Change: %s -> %s", lastRaftState, state))
				}
				lastRaftState = state

				// Log Leader changes
				leaderID := consensus.LeaderID()
				if leaderID != lastLeaderID {
					if leaderID == "" {
						msg := "Leader Election: Lost leader"
						slog.Default().LogAttrs(ctx, slog.LevelInfo, "cluster event", logattr.Message(msg))
						schedulerRPC.AddEvent(msg)
					} else {
						msg := fmt.Sprintf("Leader Election: New Leader is %s", leaderID)
						slog.Default().LogAttrs(ctx, slog.LevelInfo, "cluster event", logattr.Message(msg))
						schedulerRPC.AddEvent(msg)
					}
				}
				lastLeaderID = leaderID

				var stateVal float64
				switch state {
				case "Leader":
					stateVal = 2
				case "Candidate":
					stateVal = 1
				default:
					stateVal = 0
				}
				metrics.RaftState.Set(stateVal)

				// Update Cluster Members
				members := membership.Members()
				alive, failed, left := 0, 0, 0
				for _, m := range members {
					switch m.Status {
					case serf.StatusAlive:
						alive++
					case serf.StatusFailed:
						failed++
					case serf.StatusLeft:
						left++
					}
				}
				metrics.ClusterMembers.WithLabelValues("alive").Set(float64(alive))
				metrics.ClusterMembers.WithLabelValues("failed").Set(float64(failed))
				metrics.ClusterMembers.WithLabelValues("left").Set(float64(left))
			}
		}
	}()

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "listening for cluster events")

	<-ctx.Done()
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "shutting down")

	if pid1 != nil {
		// Reversed teardown: drain local jobs to peers (bounded), leave
		// serf gracefully, stop the raft engine, then hand the local
		// teardown (StopAll -> sync -> umount -> reboot) to the kernel
		// executor. The drain deadline derives from Background, not the
		// already-cancelled run context (Go manifesto section 11).
		drainCtx, cancel := context.WithTimeout(context.Background(), pid1.grace)
		if _, derr := sched.DrainNode(drainCtx, nodeID); derr != nil {
			slog.Default().LogAttrs(drainCtx, slog.LevelWarn, "drain during shutdown failed", logattr.Err(derr))
		}
		cancel()
		if lerr := membership.Leave(); lerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "serf leave during shutdown failed", logattr.Err(lerr))
		}
		if cerr := consensus.Shutdown(); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "raft shutdown failed", logattr.Err(cerr))
		}
		if s.agentMgr != nil {
			pid1.complete(context.Background(), s.agentMgr)
		}
	}
	select {
	case ferr := <-fatalCh:
		// Shut down because of a fault, not a signal. Returning the
		// cause is what makes goblind exit nonzero, so systemd and any
		// supervisor see a failed unit rather than a clean stop.
		return ferr
	default:
		return nil
	}
}

func handleQUICConn(conn *quic.Conn, bus eventbus.EventBus, m *cluster.Membership) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go handleQUICStream(stream, bus, m)
	}
}

// CertManager handles dynamic certificate reloading
type CertManager struct {
	mu       sync.RWMutex
	cert     *tls.Certificate
	certFile string
	keyFile  string
}

func NewCertManager(certFile, keyFile string) (*CertManager, error) {
	cm := &CertManager{
		certFile: certFile,
		keyFile:  keyFile,
	}
	if err := cm.Load(); err != nil {
		return nil, err
	}
	return cm, nil
}

func (cm *CertManager) Load() error {
	cert, err := tls.LoadX509KeyPair(cm.certFile, cm.keyFile)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	cm.cert = &cert
	cm.mu.Unlock()
	return nil
}

func (cm *CertManager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cert, nil
}

func (cm *CertManager) GetClientCertificate(req *tls.CertificateRequestInfo) (*tls.Certificate, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cert, nil
}

func (cm *CertManager) Watch(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	var lastMod time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(cm.certFile)
			if err != nil {
				continue
			}
			if info.ModTime().After(lastMod) {
				slog.Default().LogAttrs(ctx, slog.LevelInfo, "certificate change detected, reloading")
				if err := cm.Load(); err != nil {
					slog.Default().LogAttrs(ctx, slog.LevelError, "failed to reload certificate", logattr.Err(err))
				} else {
					slog.Default().LogAttrs(ctx, slog.LevelInfo, "certificate reloaded")
					lastMod = info.ModTime()
				}
			}
		}
	}
}

func handleQUICStream(stream *quic.Stream, bus eventbus.EventBus, m *cluster.Membership) {
	defer func() {
		if err := stream.Close(); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close gapi stream failed", logattr.Err(err))
		}
	}()

	// GAPI Protocol: 4-byte BigEndian length prefix + Protobuf Envelope
	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return
	}

	l := binary.BigEndian.Uint32(lenBuf[:])

	// Limit message size
	if l > 10<<20 { // 10MB limit
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "gapi message too large", logattr.Bytes(int(l)))
		return
	}

	data := make([]byte, l)
	if _, err := io.ReadFull(stream, data); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to read gapi payload", logattr.Err(err))
		return
	}

	// Handle GAPI Envelope
	var env protopkg.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to unmarshal gapi envelope", logattr.Err(err))
		return
	}

	// Route to EventBus
	// Topic format: scope/topic or just topic
	scope := ""
	topic := env.Topic
	if i := strings.IndexByte(env.Topic, '/'); i > 0 {
		scope = env.Topic[:i]
		topic = env.Topic[i+1:]
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "gapi event received", logattr.Topic(env.Topic), logattr.Scope(scope), logattr.Source(env.Source))

	// If it's a lifecycle status, log it explicitly
	if env.Type == "event" {
		// Convert proto payload to map for EventBus
		var payloadMap map[string]interface{}
		if env.Payload != nil {
			payloadMap = make(map[string]interface{})
			// TODO: proper proto-to-map conversion
			payloadMap["raw_proto_type"] = env.Payload.TypeUrl
		}

		// Stream handler is a goroutine terminus: there is no caller to
		// propagate to, so a publish failure is logged.
		if err := bus.PublishLocal("agent", topic, payloadMap, []string{"source:" + env.Source}); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "publish failed", logattr.Topic(topic), logattr.Source(env.Source), logattr.Err(err))
		}
	}
}

// generateDocsCert generates a self-signed cert for QUIC
// certDNSNames returns the SANs for the dev cert: localhost plus the
// configured node id and the resolved hostname (deduplicated).
func certDNSNames(nodeID string) []string {
	names := []string{"localhost"}
	seen := map[string]bool{"localhost": true}
	for _, n := range []string{nodeID, resolvedHostname()} {
		if n != "" && !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	return names
}

func resolvedHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// generateDocsCert builds the dev-mode self-signed cert from the real
// node identity (nodeID + resolved hostname), not a hardcoded node list
// (R21). Dev-only: production fails closed before reaching this path.
func generateDocsCert(nodeID string) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Valid for 1 year
	notBefore := time.Now().Add(-1 * time.Minute)
	notAfter := time.Now().Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0")},
		DNSNames:     certDNSNames(nodeID),
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
