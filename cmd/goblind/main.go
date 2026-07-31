package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	gapiconfig "github.com/goppydae/gapi/core/config"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/goppydae/goblin/internal/supervisor"
	"github.com/goppydae/goblin/internal/version"
	"github.com/spf13/cobra"
)

var (
	nodeID             string
	listenAddr         string
	advertiseAddr      string
	advertisePort      int
	raftDir            string
	raftSnapshotThresh uint64
	raftSnapshotInt    time.Duration
	raftTrailingLogs   uint64
	joinAddr           string
	bootstrapExpect    int
	pid1Mode           bool
	noEarlyMounts      bool
	watchdogDevice     string
	watchdogInterval   time.Duration
	shutdownGrace      time.Duration
	encryptionKey      string
	tlsCertFile        string
	tlsKeyFile         string
	tlsCAFile          string
	metricsAddr        string
	productionMode     bool
	agentVerifyKey     string
	operatorKeyFiles   []string
	logLevel           string
	logFormat          string
	logFile            string
	logLokiURL         string
	networkGateTimeout time.Duration
)

var rootCmd = &cobra.Command{
	Use:     "goblind",
	Short:   "Goblin Distributed Supervisor Daemon",
	Version: version.Version,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Detect Resources. Logged inside Run, after the configured
		// handler is installed, so the line honors --log-format.
		cpu := runtime.NumCPU()
		mem := getTotalMemory()

		tags := map[string]string{
			"cpu":    fmt.Sprintf("%d", cpu),
			"memory": fmt.Sprintf("%d", mem),
		}

		cfg := supervisor.Config{
			NodeID:                nodeID,
			ListenAddr:            listenAddr,
			AdvertiseAddr:         advertiseAddr,
			AdvertisePort:         advertisePort,
			RaftDir:               raftDir,
			RaftSnapshotThreshold: raftSnapshotThresh,
			RaftSnapshotInterval:  raftSnapshotInt,
			RaftTrailingLogs:      raftTrailingLogs,
			JoinAddr:              joinAddr,
			BootstrapExpect:       bootstrapExpect,
			Pid1Mode:              pid1Mode,
			NoEarlyMounts:         noEarlyMounts,
			WatchdogDevice:        watchdogDevice,
			WatchdogInterval:      watchdogInterval,
			ShutdownGrace:         shutdownGrace,
			Tags:                  tags,
			EncryptionKey:         encryptionKey,
			CertFile:              tlsCertFile,
			KeyFile:               tlsKeyFile,
			CAFile:                tlsCAFile,
			MetricsAddr:           metricsAddr,
			ProductionMode:        productionMode,
			AgentVerifyKey:        agentVerifyKey,
			OperatorKeyFiles:      operatorKeyFiles,
			Logging:               buildLoggingConfig(),
			NetworkGateTimeout:    networkGateTimeout,
		}
		return supervisor.New(cfg).Run(cmd.Context())
	},
}

// buildLoggingConfig mirrors gapi's LoggingConfig from goblind's flags.
// Production defaults to JSON so cluster logs are machine-parseable.
func buildLoggingConfig() gapiconfig.LoggingConfig {
	cfg := gapiconfig.LoggingConfig{Level: logLevel, Format: logFormat}
	if cfg.Format == "" {
		if productionMode {
			cfg.Format = "json"
		} else {
			cfg.Format = "console"
		}
	}
	if logFile != "" {
		cfg.File = gapiconfig.FileOutputConfig{
			Enabled:    true,
			Path:       logFile,
			MaxSize:    100, // megabytes
			MaxBackups: 5,
			MaxAge:     30, // days
			Compress:   true,
		}
	}
	if logLokiURL != "" {
		cfg.Loki = gapiconfig.LokiOutputConfig{Enabled: true, URL: logLokiURL}
	}
	return cfg
}

func getTotalMemory() uint64 {
	// Simple Linux implementation
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close /proc/meminfo failed", logattr.Err(cerr))
		}
	}()

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
	rootCmd.Flags().StringVar(&listenAddr, "listen-addr", "127.0.0.1:29000", "Single control-plane bind address (QUIC; carries GAPI, RPC, Serf, and Raft via ALPN)")
	rootCmd.Flags().StringVar(&advertiseAddr, "advertise-addr", "", "Advertise address (if different from bind)")
	rootCmd.Flags().BoolVar(&pid1Mode, "pid1", false, "Run as PID 1: kernel Phase 0 boot before the cluster stack; reversed teardown on shutdown")
	rootCmd.Flags().BoolVar(&noEarlyMounts, "no-early-mounts", false, "Skip the Phase 0 mount table (container environments)")
	rootCmd.Flags().StringVar(&watchdogDevice, "watchdog-device", "", "Hardware watchdog device to keep alive (empty: disabled)")
	rootCmd.Flags().DurationVar(&watchdogInterval, "watchdog-interval", 10*time.Second, "Watchdog keepalive kick interval")
	rootCmd.Flags().DurationVar(&shutdownGrace, "shutdown-grace", 10*time.Second, "Per-phase shutdown grace (drain + agent stop) before forcing")
	rootCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	rootCmd.Flags().Uint64Var(&raftSnapshotThresh, "raft-snapshot-threshold", 0, "Outstanding Raft log entries before a compaction snapshot (0: raft default, 8192)")
	rootCmd.Flags().DurationVar(&raftSnapshotInt, "raft-snapshot-interval", 0, "How often Raft checks whether a compaction snapshot is due (0: raft default, 120s)")
	rootCmd.Flags().Uint64Var(&raftTrailingLogs, "raft-trailing-logs", 0, "Raft log entries retained after a snapshot for fast follower replay (0: raft default, 10240)")
	rootCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")
	rootCmd.Flags().IntVar(&bootstrapExpect, "bootstrap-expect", 0, "Seed the cluster once this many nodes carrying the same value are visible; one of them is elected to bootstrap (0: seed model, the node with no --join bootstraps alone)")
	rootCmd.Flags().StringVar(&metricsAddr, "metrics-addr", "", "Prometheus metrics listen address (empty: disabled)")

	// Security Flags
	rootCmd.Flags().BoolVar(&productionMode, "production", false, "Restrict agent discovery to binaries with verified signatures")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.Flags().StringVar(&logFormat, "log-format", "", "Log format: json or console (default: json in production, console otherwise)")
	rootCmd.Flags().StringVar(&logFile, "log-file", "", "Also write logs to this file (rotated)")
	rootCmd.Flags().StringVar(&logLokiURL, "log-loki-url", "", "Forward logs to this Loki endpoint")
	rootCmd.Flags().DurationVar(&networkGateTimeout, "network-gate-timeout", 0, "Block startup until the network agent reports running, failing after this bound (0: gate disabled)")
	rootCmd.Flags().StringVar(&agentVerifyKey, "agent-verify-key", "", "Path to the Ed25519 public key for agent signature verification (falls back to $RUNTIME_VERIFY_KEY)")
	rootCmd.Flags().StringArrayVar(&operatorKeyFiles, "operator-key", nil,
		"Path to a hex-encoded Ed25519 operator public key that bootstraps the cluster's operator registry (repeatable; with none, every mutating verb is refused). Keep the matching private key: the last registered key cannot be removed, so a registry left holding only keys nobody controls can neither authorize anything nor be re-seeded, and the only recovery is wiping the data dir on EVERY replica and re-bootstrapping. Produce keys with 'goblinctl operator keygen'.")
	rootCmd.Flags().StringVar(&encryptionKey, "encrypt", "", "Base64 encoded 32-byte secret key for Serf encryption")
	rootCmd.Flags().StringVar(&tlsCertFile, "tls-cert", "", "Path to TLS certificate")
	rootCmd.Flags().StringVar(&tlsKeyFile, "tls-key", "", "Path to TLS private key")
	rootCmd.Flags().StringVar(&tlsCAFile, "tls-ca", "", "Path to CA certificate for mTLS")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
