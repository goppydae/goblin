package supervisor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/serf/serf"
	"github.com/quic-go/quic-go"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	gapiclient "github.com/goppydae/gapi/core/client"
	gapiconfig "github.com/goppydae/gapi/core/config"
)

// Config holds configuration for the Supervisor
type Config struct {
	NodeID        string
	SerfAddr      string
	SerfPort      int
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
}

// Supervisor manages the Goblin daemon components
type Supervisor struct {
	cfg Config
}

// New creates a new Supervisor
func New(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg}
}

// Run starts the supervisor and blocks until context is canceled
func (s *Supervisor) Run(ctx context.Context) error {
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
		// Default to insecure
		fmt.Println("⚠️ Security warning: Encryption disabled")
	}

	// Create Serf membership
	tags := map[string]string{
		"raft_addr": s.cfg.RaftAddr,
	}
	for k, v := range s.cfg.Tags {
		tags[k] = v
	}
	membership, err := cluster.NewMembership(nodeID, s.cfg.SerfAddr, s.cfg.SerfPort, tags, secretKey)
	if err != nil {
		return fmt.Errorf("failed to create membership: %w", err)
	}
	defer membership.Shutdown()

	fmt.Printf("✅ Serf membership initialized (%s:%d)\n", s.cfg.SerfAddr, s.cfg.SerfPort)

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
	defer consensus.Shutdown()

	fmt.Printf("✅ Raft consensus initialized (%s)\n", s.cfg.RaftAddr)

	// Create distributed event bus
	bus := eventbus.NewDistributedEventBus(nodeID, membership, consensus)

	// Subscribe to cluster events
	bus.Subscribe("cluster.node.joined", func(e eventbus.Event) {
		log.Printf("[cluster] Node joined: %v", e.Payload)
	})

	bus.Subscribe("global.alert", func(e eventbus.Event) {
		log.Printf("[alert] %v", e.Payload)
	})

	// Publish local event
	bus.PublishLocal("system", "cluster.node.announce", map[string]interface{}{
		"node_id": nodeID,
		"address": fmt.Sprintf("%s:%d", s.cfg.SerfAddr, s.cfg.SerfPort),
	}, []string{"announce"})

	fmt.Println("✅ Distributed event bus initialized")

	// Create Store
	kvStore := store.NewStore(consensus, bus)
	fmt.Println("✅ Distributed KV Store initialized")

	// Create Scheduler
	sched := scheduler.NewScheduler(kvStore, membership)
	fmt.Println("✅ Scheduler initialized")

	// Register failure handler (Leader only)
	membership.SetEventHandler(func(e serf.Event) {
		if !consensus.IsLeader() {
			return
		}
		switch e.EventType() {
		case serf.EventMemberJoin:
			me, ok := e.(serf.MemberEvent)
			if !ok {
				return
			}
			for _, m := range me.Members {
				raftAddr, ok := m.Tags["raft_addr"]
				if ok {
					log.Printf("🤝 Adding voter %s at %s to Raft", m.Name, raftAddr)
					if err := consensus.AddVoter(m.Name, raftAddr); err != nil {
						log.Printf("⚠️ Failed to add voter %s: %v", m.Name, err)
					}
				}
			}
		case serf.EventMemberFailed, serf.EventMemberLeave:
			me, ok := e.(serf.MemberEvent)
			if !ok {
				return
			}
			for _, m := range me.Members {
				log.Printf("💀 Leader detected failure of node %s. Initiating recovery...", m.Name)
				if err := sched.HandleNodeFailure(context.Background(), m.Name); err != nil {
					log.Printf("❌ Recovery failed for node %s: %v", m.Name, err)
				}
				// Also remove from Raft?
				if err := consensus.RemoveServer(m.Name); err != nil {
					log.Printf("⚠️ Failed to remove server %s from Raft: %v", m.Name, err)
				}
			}
		}
	})

	// Start Reconciliation Loop (Leader)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !consensus.IsLeader() {
					continue
				}
				// Check for failed nodes in Serf
				members := membership.Members()
				for _, m := range members {
					if m.Status == serf.StatusFailed || m.Status == serf.StatusLeft {
						log.Printf("💀 Reconciliation: Node %s is down (%s). Checking for orphaned jobs...", m.Name, m.Status)
						if err := sched.HandleNodeFailure(context.Background(), m.Name); err != nil {
							log.Printf("❌ Recovery failed for node %s: %v", m.Name, err)
						}
					}
				}
			}
		}
	}()

	// Start Job Watcher (Leader only)
	go func() {
		pendingPrefix := "/jobs/pending/"
		fmt.Println("👑 Leader Scheduler watching for pending jobs...")

		// Watch for KV changes
		ch := kvStore.Watch(ctx, "default", "") // ensure ctx is used
		for e := range ch {
			if !consensus.IsLeader() {
				continue
			}

			pl := e.Payload
			key, _ := pl["key"].(string)
			op, _ := pl["op"].(string)
			val, _ := pl["value"].(string) // Job ID

			// If new pending job
			if op == "set" && len(key) > len(pendingPrefix) && key[:len(pendingPrefix)] == pendingPrefix {
				jobID := val
				fmt.Printf("📋 Scheduler detected pending job: %s\n", jobID)

				// 1. Schedule
				job := &scheduler.Job{ID: jobID}

				targetNode, err := sched.Schedule(job, scheduler.StrategyRandom)
				if err != nil {
					log.Printf("⚠️ Failed to schedule job %s: %v", jobID, err)
					continue
				}
				fmt.Printf("🎯 Scheduling job %s to node %s\n", jobID, targetNode)

				// 2. Assign
				if err := sched.Assign(context.Background(), jobID, targetNode); err != nil {
					log.Printf("⚠️ Failed to assign job: %v", err)
					continue
				}
				fmt.Println("✅ Assignment persisted.")

				// 3. Cleanup Pending?
				// kvStore.Delete(context.Background(), "default", key)
			}
		}
	}()

	// Start Node Job Watcher (Assignment watcher)
	go func() {
		assignmentsPrefix := fmt.Sprintf("/jobs/assignments/%s/", nodeID)
		fmt.Printf("👀 Watching for jobs at %s...\n", assignmentsPrefix)

		// 1. Initialize GAPI Client
		gapiCfg, err := gapiconfig.Load()
		var client *gapiclient.Client
		if err != nil {
			log.Printf("⚠️ Failed to load GAPI config: %v. Running in localized/mock mode.", err)
		} else {
			c, err := gapiclient.New(gapiCfg)
			if err != nil {
				log.Printf("⚠️ Failed to create GAPI client: %v. Running in localized/mock mode.", err)
			} else {
				client = c
			}
		}

		// 2. Watch for KV changes
		// Watch all keys in default namespace and filter by prefix
		ch := kvStore.Watch(ctx, "default", "")

		for e := range ch {
			pl := e.Payload
			key, _ := pl["key"].(string)
			op, _ := pl["op"].(string)
			if len(key) > len(assignmentsPrefix) && key[:len(assignmentsPrefix)] == assignmentsPrefix {
				jobID := key[len(assignmentsPrefix):] // Extract Job ID from Key

				if op == "set" {
					// value is jobID, but we parsed it from key too.
					fmt.Printf("🎯 Job Assigned: %s (Key: %s)\n", jobID, key)
					log.Printf("🚀 Attempting to start agent for Job %s...", jobID)

					if client != nil {
						// Fetch Job Spec
						specKey := "/jobs/specs/" + jobID
						var opts gapiclient.LifecycleOptions

						specVal, ok, _ := kvStore.Get(context.Background(), "default", specKey)
						if ok {
							var job scheduler.Job
							if err := json.Unmarshal(specVal, &job); err == nil {
								opts.Env = job.Env
								opts.RestartPolicy = job.RestartPolicy
								log.Printf("📜 Loaded spec for %s: Env=%v, Restart=%s", jobID, opts.Env, opts.RestartPolicy)
							} else {
								log.Printf("⚠️ Failed to unmarshal job spec for %s: %v", jobID, err)
							}
						} else {
							log.Printf("⚠️ No job spec found for %s, using defaults", jobID)
						}

						results := client.StartWithOpts(context.Background(), []string{jobID}, opts)
						for _, res := range results {
							if res.Err != nil {
								log.Printf("❌ Failed to start agent %s: %v", res.AgentID, res.Err)
							} else {
								log.Printf("✅ Agent %s started (Status: %s)", res.AgentID, res.Status.State)
							}
						}
					} else {
						log.Printf("🚧 [MOCK] Starting agent %s", jobID)
					}
				} else if op == "delete" {
					fmt.Printf("🛑 Job Unassigned: %s\n", jobID)
					log.Printf("🛑 Stopping agent for Job %s...", jobID)

					if client != nil {
						results := client.Stop(context.Background(), []string{jobID})
						for _, res := range results {
							if res.Err != nil {
								log.Printf("❌ Failed to stop agent %s: %v", res.AgentID, res.Err)
							} else {
								log.Printf("✅ Agent %s stopped (Status: %s)", res.AgentID, res.Status.State)
							}
						}
					} else {
						log.Printf("🚧 [MOCK] Stopping agent %s", jobID)
					}
				}
			}
		}
	}()

	// Generate TLS for QUIC
	cert, err := generateDocsCert()
	if err != nil {
		return fmt.Errorf("failed to generate TLS cert: %w", err)
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"goblin-kv"},
	}

	// Start QUIC Server
	go func() {
		listener, err := quic.ListenAddr(s.cfg.APIAddr, tlsConf, nil)
		if err != nil {
			log.Fatalf("QUIC listen failed: %v", err) // TODO: Handle better
		}
		log.Printf("📡 QUIC KV Listener on %s...", s.cfg.APIAddr)

		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				// check if ctx is done
				select {
				case <-ctx.Done():
					return
				default:
					log.Printf("QUIC accept error: %v", err)
					continue
				}
			}
			go handleQUICConn(conn, kvStore, bus)
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

		// Start Metrics Collector
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
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
	}

	fmt.Println("📡 Listening for cluster events...")

	<-ctx.Done()
	fmt.Println("\n🛑 Shutting down...")
	return nil
}

func handleQUICConn(conn *quic.Conn, s *store.Store, bus eventbus.EventBus) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go handleQUICStream(stream, s, bus)
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

func handleQUICStream(stream *quic.Stream, s *store.Store, bus eventbus.EventBus) {
	defer stream.Close()

	// Protocol: Op(1) | NSLen(4) | NS | KeyLen(4) | Key | ValLen(4) | Val

	var op [1]byte
	if _, err := io.ReadFull(stream, op[:]); err != nil {
		return
	}

	readString := func() (string, error) {
		var lenBuf [4]byte
		if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
			return "", err
		}
		l := binary.BigEndian.Uint32(lenBuf[:])
		buf := make([]byte, l)
		if _, err := io.ReadFull(stream, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	}

	readBytes := func() ([]byte, error) {
		var lenBuf [4]byte
		if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
			return nil, err
		}
		l := binary.BigEndian.Uint32(lenBuf[:])
		buf := make([]byte, l)
		if _, err := io.ReadFull(stream, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}

	writeResp := func(status byte, val []byte) {
		stream.Write([]byte{status})
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(val)))
		stream.Write(lenBuf[:])
		stream.Write(val)
	}

	// For watch commands, we might not read NS/Key immediately if the protocol differs,
	// but to keep it simple, we assume same header format.
	namespace, err := readString()
	if err != nil {
		return
	}
	key, err := readString()
	if err != nil {
		return
	}

	ctx := context.Background()

	switch op[0] {
	case 1: // SET
		val, err := readBytes()
		if err != nil {
			return
		}
		if err := s.Set(ctx, namespace, key, val); err != nil {
			writeResp(2, []byte(err.Error()))
		} else {
			writeResp(0, nil)
		}
	case 2: // GET
		val, found, err := s.Get(ctx, namespace, key)
		if err != nil {
			writeResp(2, []byte(err.Error()))
		} else if !found {
			writeResp(1, []byte("Not found")) // 1=NotFound
		} else {
			writeResp(0, val)
		}
	case 3: // DELETE
		if err := s.Delete(ctx, namespace, key); err != nil {
			writeResp(2, []byte(err.Error()))
		} else {
			writeResp(0, nil)
		}
	case 4: // WATCH
		// Streaming response.
		ch := s.Watch(ctx, namespace, key)
		for e := range ch {
			pl := e.Payload
			msg := fmt.Sprintf("%s %s %s", pl["op"], pl["key"], pl["value"])
			writeResp(0, []byte(msg))
		}
	case 5: // SCAN
		// key is treated as prefix
		results, err := s.Scan(ctx, namespace, key)
		if err != nil {
			writeResp(2, []byte(err.Error()))
			return
		}
		// Marshal to JSON
		data, err := json.Marshal(results)
		if err != nil {
			writeResp(2, []byte(err.Error()))
		} else {
			writeResp(0, data)
		}
	}
}

// generateDocsCert generates a self-signed cert for QUIC
func generateDocsCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}
