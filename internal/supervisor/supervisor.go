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
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/serf/serf"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"
	"github.com/goppydae/goblin/core/transport"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds configuration for the Supervisor
type Config struct {
	NodeID        string
	SerfAddr      string
	AdvertiseAddr string
	AdvertisePort int
	RaftAddr      string
	RaftDir       string
	JoinAddr      string
	APIAddr       string
	Tags          map[string]string
	EncryptionKey string // Base64 encoded 32-byte key
	CertFile      string
	KeyFile       string
	CAFile        string
	MetricsAddr   string
	// ProductionMode restricts embedded-GAPI agent discovery to binaries
	// with verified signatures (review R20).
	ProductionMode bool
	// AgentVerifyKey is the path to the Ed25519 public key that verifies
	// agent-binary signatures; falls back to $RUNTIME_VERIFY_KEY.
	AgentVerifyKey string
}

// Supervisor manages the Goblin daemon components
type Supervisor struct {
	cfg      Config
	agentMgr *gapiagentmgr.AgentManager // GAPI agent manager (optional)
}

// New creates a new Supervisor
func New(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Run starts the supervisor and blocks until context is canceled
func (s *Supervisor) Run(ctx context.Context) (err error) {
	nodeID := s.cfg.NodeID
	if nodeID == "" {
		host, _ := os.Hostname()
		nodeID = host
	}

	fmt.Printf("🚀 Goblin supervisor starting as '%s'...\n", nodeID)

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
		fmt.Println("🔒 Serf encryption enabled")
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
		fmt.Println("🔒 TLS enabled (Dynamic Reloading)")

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
			fmt.Println("🔒 Raft mTLS enabled")
		} else {
			fmt.Println("⚠️ TLS enabled but no CA provided - mTLS disabled")
		}
	} else {
		// Default to insecure: Generate ephemeral cert for QUIC
		// QUIC requires a certificate even if we don't verify it (TLS 1.3)
		var err error
		cert, err := generateDocsCert()
		if err != nil {
			return fmt.Errorf("failed to generate ephemeral TLS cert: %w", err)
		}
		tlsCfg = &tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
			NextProtos:         []string{"gapi-quic", "goblin-rpc", "serf-quic"},
		}
		fmt.Println("⚠️ Security warning: Encryption disabled (using ephemeral self-signed cert)")
	}

	// Create Serf membership
	// Resolve Raft Address to ensure consistency with Consensus.Leader()
	raftAddr := s.cfg.RaftAddr
	if host, port, err := net.SplitHostPort(raftAddr); err == nil {
		if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
			// Prefer IPv4
			resolved := false
			for _, ip := range ips {
				if ip4 := ip.To4(); ip4 != nil {
					raftAddr = fmt.Sprintf("%s:%s", ip4.String(), port)
					resolved = true
					break
				}
			}
			if !resolved {
				raftAddr = fmt.Sprintf("%s:%s", ips[0].String(), port)
			}
			fmt.Printf("ℹ️ Resolved raft address '%s' to '%s'\n", s.cfg.RaftAddr, raftAddr)
		}
	}

	tags := map[string]string{
		"raft_addr":   raftAddr,
		"api_addr":    s.cfg.APIAddr, // Broadcast API address
		"schema_hash": "v1-proto-hash",
		"version":     "0.3.3",
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
			fmt.Printf("ℹ️ Resolved advertise address '%s' to '%s'\n", s.cfg.AdvertiseAddr, advAddr)
		}
	}
	// Create QUIC Serf Transport
	// Important: We use 0.0.0.0 or bind address.
	serfHost, serfPortStr, err := net.SplitHostPort(s.cfg.SerfAddr)
	if err != nil {
		return fmt.Errorf("invalid serf address %q: %w", s.cfg.SerfAddr, err)
	}
	serfPort, err := strconv.Atoi(serfPortStr)
	if err != nil {
		return fmt.Errorf("invalid serf port: %w", err)
	}

	serfTransport, err := transport.NewQUICSerfTransport(s.cfg.SerfAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("failed to create QUIC serf transport: %w", err)
	}

	// ... (Wait, need to pass transport to NewMembership)

	membership, err := cluster.NewMembership(nodeID, serfHost, serfPort, advAddr, s.cfg.AdvertisePort, tags, secretKey, serfTransport)
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

	fmt.Printf("✅ Serf membership initialized (%s) [QUIC]\n", s.cfg.SerfAddr)

	// Join if requested
	if s.cfg.JoinAddr != "" {
		if err := membership.Join([]string{s.cfg.JoinAddr}); err != nil {
			log.Printf("⚠️ Failed to join cluster at %s: %v", s.cfg.JoinAddr, err)
		} else {
			fmt.Printf("✅ Joined cluster via %s\n", s.cfg.JoinAddr)
		}
	}

	// Create Raft consensus
	consensus, err := consensus.NewConsensus(nodeID, s.cfg.RaftDir, s.cfg.RaftAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("failed to create consensus: %w", err)
	}
	defer func() {
		if serr := consensus.Shutdown(); serr != nil && err == nil {
			err = fmt.Errorf("shutdown consensus: %w", serr)
		}
	}()

	fmt.Printf("✅ Raft consensus initialized (%s)\n", s.cfg.RaftAddr)

	// Create distributed event bus
	bus := eventbus.NewDistributedEventBus(nodeID, membership, consensus)

	// Bridge Serf events to EventBus
	membership.SetEventHandler(func(e serf.Event) {
		switch ev := e.(type) {
		case serf.MemberEvent:
			for _, member := range ev.Members {
				var topic string
				switch ev.EventType() {
				case serf.EventMemberJoin:
					topic = "cluster.node.joined"
					// Auto-join Raft if Leader. Retried with backoff: a
					// leader change between the Serf event and the AddVoter
					// commit must not silently orphan the voter (R6). The
					// goroutine keeps the Serf dispatch goroutine unblocked.
					if consensus.IsLeader() {
						raftAddr, ok := member.Tags["raft_addr"]
						if ok {
							log.Printf("Cluster: Adding Raft voter %s at %s", member.Name, raftAddr)
							go func(name, addr string) {
								if err := addVoterWithRetry(context.Background(), consensus, name, addr); err != nil {
									log.Printf("Failed to add voter %s: %v", name, err)
								}
							}(member.Name, raftAddr)
						}
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
						log.Printf("publish %s for %s: %v", topic, member.Name, err)
					}
				}
			}
		}
	})

	// Create Store
	kvStore := store.NewStore(consensus, bus)
	fmt.Println("✅ Distributed KV Store initialized")

	// Create Scheduler
	sched := scheduler.NewScheduler(kvStore, membership, bus, consensus.IsLeader, func(addr string) (scheduler.RPCClient, error) {
		// Use the same QUIC client logic as CLI (but internal)
		// We need to access tlsCfg here.
		// Note: addr should be "host:port" of the QUIC listener (APIAddr)
		// But s.getNodeAddress returns "host:port" from Serf.
		// Serf port != API port usually?
		// Goblin architecture: APIAddr is for RPC. Serf is for Gossip.
		// We need to know the API Addr of the target node.
		// Currently Serf tags don't explicitly broadcast API Addr?
		// Wait, Config.APIAddr is local.
		// We should add "api_addr" to Serf tags!
		//
		// For MVP, let's assume API Port is standard or same as Serf+offset?
		// Or assume the address in Serf Member.Addr is usable if we use default API port?
		//
		// Let's use the address passed in.
		return NewQUICRPCClient(addr, tlsCfg)
	})
	fmt.Println("✅ Scheduler initialized")

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
		log.Printf("⚠️  Agent discovery warning: %v", err)
	} else {
		log.Printf("✅ Local agent manager initialized (%d agents discovered)", len(discovered))
	}

	s.agentMgr = agentMgr

	// Register Scheduler RPC for CLI operations
	schedulerRPC := &SchedulerRPC{
		scheduler:  sched,
		membership: membership,
		consensus:  consensus,
		agentMgr:   agentMgr, // Wire agent manager to RPC
	}
	fmt.Println("✅ Scheduler RPC created")

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
				log.Printf("[cluster] %s", msg)
				schedulerRPC.AddEvent(msg)
			}
		})
	}

	bus.Subscribe("global.alert", func(e eventbus.Event) {
		msg := fmt.Sprintf("Alert: %v", e.Payload)
		log.Printf("[alert] %s", msg)
		schedulerRPC.AddEvent(msg)
	})

	// Create QUIC RPC Handler
	quicServer := NewQUICRPCServer()

	// Register all scheduler handlers
	RegisterSchedulerHandlers(quicServer, schedulerRPC)

	// Register Node handlers
	nodeRPC := &NodeRPC{agentMgr: agentMgr}
	RegisterNodeHandlers(quicServer, nodeRPC)

	fmt.Println("✅ Scheduler & Node RPC handlers registered")

	// Prepare TLS for QUIC API
	// tlsCfg is now guaranteed to be non-nil (loaded from file or generated ephemeral)
	// We ensure API protocols are present.

	// Ensure ALPN is set on the loaded config
	// We create a clone to avoid concurrent modification issues if shared
	cfgCopy := tlsCfg.Clone()

	// Append API protocols if not present
	// We just reset NextProtos to be safe, ensuring all are there including API ones.
	// Actually, we should merge.
	// But let's just ensure gapi-quic and goblin-rpc are there.
	// Since we set "serf-quic" earlier for Serf, we should keep it or just ensure API ones are added?
	// QUIC listener for API uses "gapi-quic" and "goblin-rpc".
	// The shared tlsCfg might be used by Serf? Serf uses its own clone in NewQUICSerfTransport.
	// So we can just set NextProtos for this specific listener.
	cfgCopy.NextProtos = []string{"gapi-quic", "goblin-rpc"}

	// API listener should NOT require client certificates (unlike likely Raft/Serf mTLS in secure mode)
	// Unless we want mutual auth for API? Usually not for CLI.
	cfgCopy.ClientAuth = tls.NoClientCert
	cfgCopy.ClientCAs = nil
	tlsCfg = cfgCopy

	// Start QUIC Server (Single Listener)
	go func() {
		// Use QUIC configuration
		quicConfig := &quic.Config{
			MaxIdleTimeout:  60 * time.Second,
			KeepAlivePeriod: 10 * time.Second,
		}

		listener, err := quic.ListenAddr(s.cfg.APIAddr, tlsCfg, quicConfig)
		if err != nil {
			log.Fatalf("QUIC listen failed: %v", err)
		}
		log.Printf("📡 GAPI & RPC Listener on %s...", s.cfg.APIAddr)

		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					log.Printf("QUIC accept error: %v", err)
					continue
				}
			}

			// ALPN Routing
			switch conn.ConnectionState().TLS.NegotiatedProtocol {
			case "goblin-rpc":
				go quicServer.HandleConnection(conn)
			case "gapi-quic":
				go handleQUICConn(conn, bus, membership)
			default:
				log.Printf("Unknown ALPN protocol: %s", conn.ConnectionState().TLS.NegotiatedProtocol)
				if err := conn.CloseWithError(0, "unknown protocol"); err != nil {
					log.Printf("close unknown-protocol connection: %v", err)
				}
			}
		}
	}()
	// Start Metrics Server
	if s.cfg.MetricsAddr != "" {
		go func() {
			http.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
			log.Printf("📊 Metrics server listening on %s", s.cfg.MetricsAddr)
			if err := http.ListenAndServe(s.cfg.MetricsAddr, nil); err != nil {
				log.Printf("⚠️ Metrics server failed: %v", err)
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
						log.Printf("[cluster] %s", msg)
						schedulerRPC.AddEvent(msg)
					} else {
						msg := fmt.Sprintf("Leader Election: New Leader is %s", leaderID)
						log.Printf("[cluster] %s", msg)
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

	fmt.Println("📡 Listening for cluster events...")

	<-ctx.Done()
	fmt.Println("\n🛑 Shutting down...")
	return nil
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
				log.Printf("🔄 Detected certificate change, reloading...")
				if err := cm.Load(); err != nil {
					log.Printf("❌ Failed to reload cert: %v", err)
				} else {
					log.Printf("✅ Certificate reloaded successfully")
					lastMod = info.ModTime()
				}
			}
		}
	}
}

func handleQUICStream(stream *quic.Stream, bus eventbus.EventBus, m *cluster.Membership) {
	defer func() {
		if err := stream.Close(); err != nil {
			log.Printf("close GAPI stream: %v", err)
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
		log.Printf("GAPI message too large: %d", l)
		return
	}

	data := make([]byte, l)
	if _, err := io.ReadFull(stream, data); err != nil {
		log.Printf("Failed to read GAPI payload: %v", err)
		return
	}

	// Handle GAPI Envelope
	var env protopkg.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		log.Printf("Failed to unmarshal GAPI envelope: %v", err)
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

	log.Printf("📨 [GAPI] Received Event: %s (Scope: %s, Source: %s)", env.Topic, scope, env.Source)

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
			log.Printf("publish %s from %s: %v", topic, env.Source, err)
		}
	}
}

// generateDocsCert generates a self-signed cert for QUIC
func generateDocsCert() (tls.Certificate, error) {
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
		DNSNames:     []string{"localhost", "node-1", "node-2", "node-3", "node-4", "node-5", "controller"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
