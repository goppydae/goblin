package cli

import (
	"fmt"
	"net/rpc"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/goppydae/gapi/core/tui"
	gapicli "github.com/goppydae/gapi/pkg/cli"
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
	RootCmd.AddCommand(publishCmd)
	RootCmd.AddCommand(tuiCmd)
	RootCmd.AddCommand(jobCmd)
	jobCmd.AddCommand(runCmd)
	jobCmd.AddCommand(drainCmd)
	jobCmd.AddCommand(migrateCmd)

	// Global flags
	RootCmd.PersistentFlags().StringVar(&apiAddr, "api-addr", "127.0.0.1:9000", "API address")

	// Create agent subcommand for GAPI operations
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Local agent management operations",
	}
	RootCmd.AddCommand(agentCmd)

	// Mount GAPI Commands under agent subcommand
	for _, cmd := range gapicli.GetRoot().Commands() {
		if cmd.Use == "tui" {
			cmd.Use = "tui"
			cmd.Short = "Local Agent TUI"
		}
		if cmd.Name() == "version" {
			continue // Avoid conflict
		}
		agentCmd.AddCommand(cmd)
	}
}

var runFile string

var (
	apiAddr string
)

var runCmd = &cobra.Command{
	Use:   "run <job-file.yaml>",
	Short: "Submit a job to the cluster (YAML or JSON)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read job spec from YAML/JSON file
		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("failed to read job file: %w", err)
		}

		// Parse YAML/JSON
		var job map[string]interface{}
		if err := yaml.Unmarshal(data, &job); err != nil {
			return fmt.Errorf("failed to parse job spec: %w", err)
		}

		// Connect to QUIC RPC
		client, err := NewQUICRPCClient(apiAddr)
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", apiAddr, err)
		}
		defer client.Close()

		// Call SchedulerRPC.SubmitJob
		var resp string
		if err := client.Call("SchedulerRPC.SubmitJob", job, &resp); err != nil {
			return fmt.Errorf("job submission failed: %w", err)
		}

		fmt.Println(resp)
		return nil
	},
}

var jobCmd = &cobra.Command{
	Use:   "job",
	Short: "Job management operations",
}

var drainCmd = &cobra.Command{
	Use:   "drain <node-id>",
	Short: "Drain all jobs from a node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		nodeID := args[0]

		// Connect to QUIC RPC
		client, err := NewQUICRPCClient(apiAddr)
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", apiAddr, err)
		}
		defer client.Close()

		// Call SchedulerRPC.DrainNode
		var migratedJobs []string
		if err := client.Call("SchedulerRPC.DrainNode", &nodeID, &migratedJobs); err != nil {
			return fmt.Errorf("drain failed: %w", err)
		}

		fmt.Printf("✅ Drained %d jobs from node %s\n", len(migratedJobs), nodeID)
		for _, jobID := range migratedJobs {
			fmt.Printf("  - %s\n", jobID)
		}
		return nil
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate <job-id> <to-node>",
	Short: "Migrate a job to another node",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := map[string]string{
			"JobID":  args[0],
			"ToNode": args[1],
		}

		// Connect to QUIC RPC
		client, err := NewQUICRPCClient(apiAddr)
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", apiAddr, err)
		}
		defer client.Close()

		// Call SchedulerRPC.MigrateJob
		var resp string
		if err := client.Call("SchedulerRPC.MigrateJob", req, &resp); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}

		fmt.Println(resp)
		return nil
	},
}

// ... generateDocsCert ...

// ... quicRequest ...

// TODO: statusCmd and publishCmd still use legacy OpCode protocol
// These should be migrated to use Serf API or GAPI EventBus

// publishCmd broadcasts events to cluster using Serf UserEvent API
var publishCmd = &cobra.Command{
	Use:   "publish <topic> [payload_json]",
	Short: "Publish a distributed event via Serf",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		topic := args[0]
		payload := []byte("{}")
		if len(args) > 1 {
			payload = []byte(args[1])
		}

		// Connect to Serf RPC
		host := apiAddr
		if idx := strings.LastIndex(apiAddr, ":"); idx > 0 {
			host = apiAddr[:idx]
		}
		serfRPCAddr := fmt.Sprintf("%s:7373", host)

		client, err := rpc.Dial("tcp", serfRPCAddr)
		if err != nil {
			return fmt.Errorf("failed to connect to Serf RPC at %s: %w", serfRPCAddr, err)
		}
		defer client.Close()

		// Send UserEvent
		req := map[string]interface{}{
			"Name":     topic,
			"Payload":  payload,
			"Coalesce": false,
		}
		var resp interface{}
		if err := client.Call("Serf.UserEvent", req, &resp); err != nil {
			return fmt.Errorf("failed to publish event: %w", err)
		}

		fmt.Printf("✅ Published event '%s' to cluster\n", topic)
		return nil
	},
}

// statusCmd queries cluster status via Serf RPC
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Goblin cluster status",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Connect to QUIC RPC (using apiAddr which defaults to 9000)
		client, err := NewQUICRPCClient(apiAddr)
		if err != nil {
			return fmt.Errorf("failed to connect to QUIC RPC at %s: %w", apiAddr, err)
		}
		defer client.Close()

		// Call SchedulerRPC.Members
		var members []supervisor.MemberInfo
		if err := client.Call("SchedulerRPC.Members", struct{}{}, &members); err != nil {
			return fmt.Errorf("failed to get members: %w", err)
		}

		fmt.Printf("✅ Connected to %s (QUIC)\n", apiAddr)
		fmt.Printf("Cluster Members: %d\n", len(members))
		fmt.Println("NAME\t\tADDRESS\t\t\tSTATUS\t\tROLE\t\tTAGS")
		fmt.Println("----\t\t-------\t\t\t------\t\t----\t\t----")
		for _, m := range members {
			tags := ""
			for k, v := range m.Tags {
				tags += fmt.Sprintf("%s=%s ", k, v)
			}
			role := "follower"
			if m.Leader {
				role = "LEADER"
			}
			fmt.Printf("%s\t%s\t\t%s\t%s\t\t%s\n", m.Name, m.Addr, m.Status, role, tags)
		}
		return nil
	},
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Unified cluster and agent TUI",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Use apiAddr for both GAPI and Cluster RPC since they now share the same QUIC endpoint
		ctrl := NewUnifiedController(apiAddr, apiAddr)
		return tui.Run(ctrl)
	},
}
