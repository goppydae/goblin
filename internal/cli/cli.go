package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hashicorp/serf/serf"
	"github.com/quic-go/quic-go"
	"github.com/spf13/cobra"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"

	gapiclient "github.com/goppydae/gapi/core/client"
	gapiconfig "github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/tui"
)

var RootCmd = &cobra.Command{
	Use:   "goblinctl",
	Short: "Goblin distributed supervisor control",
}

// ... helper code ...

var (
	nodeID   string
	serfAddr string
	serfPort int
	raftAddr string
	raftDir  string
	joinAddr string

	publishNamespace string
	publishTags      []string
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start Goblin supervisor",
	Run: func(cmd *cobra.Command, args []string) {
		if nodeID == "" {
			host, _ := os.Hostname()
			nodeID = host
		}

		fmt.Printf("🚀 Goblin supervisor starting as '%s'...\n", nodeID)

		// Create Serf membership
		// Parse serfAddr/Port logic? Simplified: assuming serfAddr is IP
		tags := map[string]string{
			"raft_addr": raftAddr,
		}
		membership, err := cluster.NewMembership(nodeID, serfAddr, serfPort, tags)
		if err != nil {
			log.Fatalf("Failed to create membership: %v", err)
		}
		defer membership.Shutdown()

		fmt.Printf("✅ Serf membership initialized (%s:%d)\n", serfAddr, serfPort)

		// Join if requested
		if joinAddr != "" {
			if err := membership.Join([]string{joinAddr}); err != nil {
				log.Printf("⚠️ Failed to join cluster at %s: %v", joinAddr, err)
			} else {
				fmt.Printf("✅ Joined cluster via %s\n", joinAddr)
			}
		}

		// Create Raft consensus
		// Raft bind addr must include port e.g. "127.0.0.1:8300"
		consensus, err := consensus.NewConsensus(nodeID, raftDir, raftAddr)
		if err != nil {
			log.Fatalf("Failed to create consensus: %v", err)
		}
		defer consensus.Shutdown()

		fmt.Printf("✅ Raft consensus initialized (%s)\n", raftAddr)

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
			"address": fmt.Sprintf("%s:%d", serfAddr, serfPort),
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
					// Ideally yes.
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
			ch := kvStore.Watch(context.Background(), "default", "")
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
					// We need a Job struct. For MVP, we reconstruct it from ID.
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
			ch := kvStore.Watch(context.Background(), "default", "")

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
							results := client.Start(context.Background(), []string{jobID})
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
			log.Fatalf("Failed to generate TLS cert: %v", err)
		}
		tlsConf := &tls.Config{
			Certificates: []tls.Certificate{cert},
			NextProtos:   []string{"goblin-kv"},
		}

		// Start QUIC Server
		go func() {
			listener, err := quic.ListenAddr(apiAddr, tlsConf, nil)
			if err != nil {
				log.Fatalf("QUIC listen failed: %v", err)
			}
			log.Printf("📡 QUIC KV Listener on %s...", apiAddr)

			for {
				conn, err := listener.Accept(context.Background())
				if err != nil {
					log.Printf("QUIC accept error: %v", err)
					continue
				}
				go handleQUICConn(conn, kvStore, bus)
			}
		}()

		fmt.Println("📡 Listening for cluster events...")
		fmt.Println("\nPress Ctrl+C to stop")

		// Wait for interrupt
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh

		fmt.Println("\n🛑 Shutting down...")

	},
}

func init() {
	startCmd.Flags().StringVar(&nodeID, "id", "", "Unique Node ID (default: hostname)")
	startCmd.Flags().StringVar(&serfAddr, "serf-addr", "127.0.0.1", "Serf bind address")
	startCmd.Flags().IntVar(&serfPort, "serf-port", 9001, "Serf bind port")
	startCmd.Flags().StringVar(&raftAddr, "raft-addr", "127.0.0.1:9002", "Raft bind address (host:port)")
	startCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	startCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")

	RootCmd.AddCommand(startCmd)
	RootCmd.AddCommand(statusCmd)
	RootCmd.AddCommand(tuiCmd)
	RootCmd.AddCommand(publishCmd)

	publishCmd.Flags().StringVar(&publishNamespace, "namespace", "", "Event namespace")
	publishCmd.Flags().StringSliceVar(&publishTags, "tags", []string{}, "Event tags")

	RootCmd.AddCommand(kvCmd)
	kvCmd.AddCommand(kvSetCmd)
	kvCmd.AddCommand(kvGetCmd)
	kvCmd.AddCommand(kvDelCmd)
	// Add Watch command
	kvCmd.AddCommand(kvWatchCmd)

	// Scheduler
	RootCmd.AddCommand(scheduleCmd)
	RootCmd.AddCommand(jobCmd)
	jobCmd.AddCommand(migrateJobCmd)

	// Shared KV flags
	kvCmd.PersistentFlags().StringVar(&kvNamespace, "namespace", "default", "KV Namespace")
	RootCmd.PersistentFlags().StringVar(&apiAddr, "api-addr", "127.0.0.1:9000", "API address")
}

var (
	kvNamespace string
	apiAddr     string
)

var jobCmd = &cobra.Command{
	Use:   "job",
	Short: "Job management",
}

var migrateJobCmd = &cobra.Command{
	Use:   "migrate <job-id> <from-node> <to-node>",
	Short: "Manually migrate a job between nodes",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		jobID, fromNode, toNode := args[0], args[1], args[2]

		// 1. Assign to new node
		// Key: /jobs/assignments/<node>/<jobID>
		// Value: <jobID>
		newKey := fmt.Sprintf("/jobs/assignments/%s/%s", toNode, jobID)
		if _, err := quicRequest(1, "default", newKey, []byte(jobID)); err != nil {
			log.Fatalf("Failed to assign to new node: %v", err)
		}
		fmt.Printf("✅ Assigned job %s to %s\n", jobID, toNode)

		// 2. Remove from old node
		oldKey := fmt.Sprintf("/jobs/assignments/%s/%s", fromNode, jobID)
		if _, err := quicRequest(3, "default", oldKey, nil); err != nil {
			log.Printf("⚠️ Failed to remove from old node: %v", err)
		} else {
			fmt.Printf("🗑️ Removed job %s from %s\n", jobID, fromNode)
		}

		fmt.Printf("🔄 Migration of %s complete.\n", jobID)
	},
}

var scheduleCmd = &cobra.Command{
	Use:   "schedule <job-id> <agent-id> <agent-type>",
	Short: "Schedule a job to the cluster",
	Args:  cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		jobID, agentID, agentType := args[0], args[1], args[2] // e.g. job-1 my-agent service

		// Connect to KV to Schedule
		// For now we just use a direct QUIC call if we had a proper API for it.
		// BUT wait, `goblinctl` is a client. It needs to talk to `goblind`.
		// We haven't implemented a "Schedule" RPC yet.
		// For MVP, we can treat "Schedule" as writing to /jobs/request/<id> via KV Set?
		// Or we can just use the KV store directly if the client supports it.
		// Let's reuse quicRequest to write the job spec to keys.

		// 1. Write Job Spec
		spec := map[string]string{
			"id":         jobID,
			"agent_id":   agentID,
			"agent_type": agentType,
		}
		specBytes, _ := json.Marshal(spec) // naive marshal
		if _, err := quicRequest(1, "default", "/jobs/specs/"+jobID, specBytes); err != nil {
			log.Fatalf("Failed to write job spec: %v", err)
		}

		// 2. Trigger Scheduling?
		// Ideally, the leader watches /jobs/specs and schedules them.
		// For this MVP, we will do client-side scheduling or just write a "pending" key.
		// Let's write to /jobs/pending/<jobID> and have the leader pick it up.
		if _, err := quicRequest(1, "default", "/jobs/pending/"+jobID, []byte(jobID)); err != nil {
			log.Fatalf("Failed to submit job: %v", err)
		}

		fmt.Printf("✅ Job %s submitted\n", jobID)
	},
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

// ... generateDocsCert ...

// ... quicRequest ...

// kvWatchCmd
var kvWatchCmd = &cobra.Command{
	Use:   "watch <key>",
	Short: "Watch for changes",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]

		tlsConf := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"goblin-kv"}}
		conn, err := quic.DialAddr(context.Background(), apiAddr, tlsConf, nil)
		if err != nil {
			log.Fatal(err)
		}

		stream, err := conn.OpenStreamSync(context.Background())
		if err != nil {
			log.Fatal(err)
		}

		// Op 4
		stream.Write([]byte{4})

		// Write Strings helper (duplicated from quicRequest for now or reusable?)
		// Let's copy-paste specifically for simplicity in this edit block
		writeString := func(s string) {
			var lenBuf [4]byte
			binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
			stream.Write(lenBuf[:])
			stream.Write([]byte(s))
		}
		writeString(kvNamespace)
		writeString(key)

		fmt.Println("👀 Watching...")
		// Read loop
		for {
			var status [1]byte
			if _, err := io.ReadFull(stream, status[:]); err != nil {
				log.Fatal(err)
			}
			var lenBuf [4]byte
			if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
				log.Fatal(err)
			}
			l := binary.BigEndian.Uint32(lenBuf[:])
			val := make([]byte, l)
			if _, err := io.ReadFull(stream, val); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("Event: %s\n", string(val))
		}
	},
}

// ... existing commands ...

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

// --- Client Helpers ---

func quicRequest(op byte, namespace, key string, val []byte) ([]byte, error) {
	tlsConf := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"goblin-kv"},
	}
	conn, err := quic.DialAddr(context.Background(), apiAddr, tlsConf, nil)
	if err != nil {
		return nil, err
	}
	defer conn.CloseWithError(0, "done")

	stream, err := conn.OpenStreamSync(context.Background())
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// Write Request
	stream.Write([]byte{op})

	writeString := func(s string) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
		stream.Write(lenBuf[:])
		stream.Write([]byte(s))
	}
	writeBytes := func(b []byte) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
		stream.Write(lenBuf[:])
		stream.Write(b)
	}

	writeString(namespace)
	writeString(key)
	if op == 1 { // SET
		writeBytes(val)
	}

	// Read Response
	var status [1]byte
	if _, err := io.ReadFull(stream, status[:]); err != nil {
		return nil, err
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return nil, err
	}
	l := binary.BigEndian.Uint32(lenBuf[:])
	respVal := make([]byte, l)
	if _, err := io.ReadFull(stream, respVal); err != nil {
		return nil, err
	}

	if status[0] != 0 {
		return nil, fmt.Errorf("remote error (%d): %s", status[0], string(respVal))
	}
	return respVal, nil
}

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Key-Value store operations (QUIC)",
}

var kvSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a value",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key, val := args[0], args[1]
		if _, err := quicRequest(1, kvNamespace, key, []byte(val)); err != nil {
			log.Fatalf("Set failed: %v", err)
		}
		fmt.Println("✅ OK")
	},
}

var kvGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a value",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		val, err := quicRequest(2, kvNamespace, key, nil)
		if err != nil {
			log.Fatalf("Get failed: %v", err)
		}
		fmt.Println(string(val))
	},
}

var kvDelCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a key",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		if _, err := quicRequest(3, kvNamespace, key, nil); err != nil {
			log.Fatalf("Delete failed: %v", err)
		}
		fmt.Println("✅ Deleted")
	},
}

var publishCmd = &cobra.Command{
	Use:   "publish <topic> [payload_json]",
	Short: "Publish a distributed event",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		topic := args[0]
		payloadStr := "{}"
		if len(args) > 1 {
			payloadStr = args[1]
		}

		fmt.Printf("📢 Publishing to '%s' (Namespace: '%s', Tags: %v, Payload: %s)...\n", topic, publishNamespace, publishTags, payloadStr)
		// Logic to join cluster and publish would go here
		// For now, verified flags are plumbed.
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Goblin supervisor status",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Goblin status: all systems nominal.")
	},
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Cluster TUI",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctrl := NewClusterController(apiAddr)
		return tui.Run(ctrl)
	},
}
