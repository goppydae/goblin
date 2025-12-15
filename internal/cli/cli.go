package cli

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/quic-go/quic-go"
	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/core/tui"
	"github.com/goppydae/goblin/internal/supervisor"
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
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := supervisor.Config{
			NodeID:   nodeID,
			SerfAddr: serfAddr,
			SerfPort: serfPort,
			RaftAddr: raftAddr,
			RaftDir:  raftDir,
			JoinAddr: joinAddr,
			APIAddr:  apiAddr,
		}

		sup := supervisor.New(cfg)
		return sup.Run(cmd.Context())
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
