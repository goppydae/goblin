package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/goppydae/goblin/internal/supervisor"
	"github.com/spf13/cobra"
)

var (
	nodeID        string
	serfAddr      string
	serfPort      int
	raftAddr      string
	raftDir       string
	joinAddr      string
	apiAddr       string
	encryptionKey string
	certFile      string
	keyFile       string
	caFile        string
	metricsAddr   string
)

var rootCmd = &cobra.Command{
	Use:   "goblind",
	Short: "Goblin Distributed Supervisor Daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Detect Resources
		cpu := runtime.NumCPU()
		mem := getTotalMemory()

		fmt.Printf("🖥️  Detected Resources: CPU=%d Cores, Memory=%d MB\n", cpu, mem/1024/1024)

		tags := map[string]string{
			"cpu":    fmt.Sprintf("%d", cpu),
			"memory": fmt.Sprintf("%d", mem),
		}

		cfg := supervisor.Config{
			NodeID:        nodeID,
			SerfAddr:      serfAddr,
			SerfPort:      serfPort,
			RaftAddr:      raftAddr,
			RaftDir:       raftDir,
			JoinAddr:      joinAddr,
			APIAddr:       apiAddr,
			Tags:          tags,
			EncryptionKey: encryptionKey,
			CertFile:      certFile,
			KeyFile:       keyFile,
			CAFile:        caFile,
			MetricsAddr:   metricsAddr,
		}
		return supervisor.New(cfg).Run(cmd.Context())
	},
}

func getTotalMemory() uint64 {
	// Simple Linux implementation
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[0] == "MemTotal:" {
			kb, _ := strconv.ParseUint(parts[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}

func init() {
	rootCmd.Flags().StringVar(&nodeID, "id", "", "Unique Node ID (default: hostname)")
	rootCmd.Flags().StringVar(&serfAddr, "serf-addr", "127.0.0.1", "Serf bind address")
	rootCmd.Flags().IntVar(&serfPort, "serf-port", 9001, "Serf bind port")
	rootCmd.Flags().StringVar(&raftAddr, "raft-addr", "127.0.0.1:9002", "Raft bind address (host:port)")
	rootCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	rootCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")
	rootCmd.Flags().StringVar(&apiAddr, "api-addr", "127.0.0.1:9000", "API address")

	// Security Flags
	rootCmd.Flags().StringVar(&encryptionKey, "encrypt", "", "Base64 encoded 32-byte secret key for Serf encryption")
	rootCmd.Flags().StringVar(&certFile, "cert", "", "Path to TLS certificate")
	rootCmd.Flags().StringVar(&keyFile, "key", "", "Path to TLS private key")
	rootCmd.Flags().StringVar(&caFile, "ca", "", "Path to CA certificate for mTLS")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
