package main

import (
	"fmt"
	"os"

	"github.com/goppydae/goblin/internal/supervisor"
	"github.com/spf13/cobra"
)

var (
	nodeID   string
	serfAddr string
	serfPort int
	raftAddr string
	raftDir  string
	joinAddr string
	apiAddr  string
)

var rootCmd = &cobra.Command{
	Use:   "goblind",
	Short: "Goblin Distributed Supervisor Daemon",
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
		return supervisor.New(cfg).Run(cmd.Context())
	},
}

func init() {
	rootCmd.Flags().StringVar(&nodeID, "id", "", "Unique Node ID (default: hostname)")
	rootCmd.Flags().StringVar(&serfAddr, "serf-addr", "127.0.0.1", "Serf bind address")
	rootCmd.Flags().IntVar(&serfPort, "serf-port", 9001, "Serf bind port")
	rootCmd.Flags().StringVar(&raftAddr, "raft-addr", "127.0.0.1:9002", "Raft bind address (host:port)")
	rootCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	rootCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")
	rootCmd.Flags().StringVar(&apiAddr, "api-addr", "127.0.0.1:9000", "API address")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
