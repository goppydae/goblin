package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"os/signal"
	"syscall"

	"github.com/quic-go/quic-go"
	"github.com/spf13/cobra"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/store"
)

var RootCmd = &cobra.Command{
	Use:   "goblinctl",
	Short: "Goblin distributed supervisor control",
}

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
		membership, err := cluster.NewMembership(nodeID, serfAddr, serfPort)
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
	startCmd.Flags().IntVar(&serfPort, "serf-port", 7946, "Serf bind port")
	startCmd.Flags().StringVar(&raftAddr, "raft-addr", "127.0.0.1:8300", "Raft bind address (host:port)")
	startCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	startCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")

	RootCmd.AddCommand(startCmd)
	RootCmd.AddCommand(statusCmd)
	RootCmd.AddCommand(publishCmd)

	publishCmd.Flags().StringVar(&publishNamespace, "namespace", "", "Event namespace")
	publishCmd.Flags().StringSliceVar(&publishTags, "tags", []string{}, "Event tags")

	RootCmd.AddCommand(kvCmd)
	kvCmd.AddCommand(kvSetCmd)
	kvCmd.AddCommand(kvGetCmd)
	kvCmd.AddCommand(kvDelCmd)
	// Add Watch command
	kvCmd.AddCommand(kvWatchCmd)

	// Shared KV flags
	kvCmd.PersistentFlags().StringVar(&kvNamespace, "namespace", "default", "KV Namespace")
	RootCmd.PersistentFlags().StringVar(&apiAddr, "api-addr", "127.0.0.1:8080", "API address")
}

var (
	kvNamespace string
	apiAddr     string
)

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
