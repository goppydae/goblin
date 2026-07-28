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

	gapiconfig "github.com/goppydae/gapi/core/config"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/goppydae/goblin/internal/supervisor"
	"github.com/goppydae/goblin/internal/version"
	"github.com/spf13/cobra"
)

var (
	nodeID   string
	serfAddr string
	// serfPort          int - Removed, now part of serfAddr
	serfAdvertiseAddr string
	serfAdvertisePort int
	raftAddr          string
	raftDir           string
	joinAddr          string
	apiAddr           string
	encryptionKey     string
	tlsCertFile       string
	tlsKeyFile        string
	tlsCAFile         string
	metricsAddr       string
	productionMode    bool
	agentVerifyKey    string
	logLevel          string
	logFormat         string
	logFile           string
	logLokiURL        string
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
			NodeID:         nodeID,
			SerfAddr:       serfAddr,
			AdvertiseAddr:  serfAdvertiseAddr,
			AdvertisePort:  serfAdvertisePort,
			RaftAddr:       raftAddr,
			RaftDir:        raftDir,
			JoinAddr:       joinAddr,
			APIAddr:        apiAddr,
			Tags:           tags,
			EncryptionKey:  encryptionKey,
			CertFile:       tlsCertFile,
			KeyFile:        tlsKeyFile,
			CAFile:         tlsCAFile,
			MetricsAddr:    metricsAddr,
			ProductionMode: productionMode,
			AgentVerifyKey: agentVerifyKey,
			Logging:        buildLoggingConfig(),
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
	rootCmd.Flags().StringVar(&serfAddr, "serf-addr", "127.0.0.1:29010", "Serf bind address (host:port)")
	// --serf-port flag removed, now part of --serf-addr
	rootCmd.Flags().StringVar(&serfAdvertiseAddr, "serf-advertise-addr", "", "Serf advertise address (if different from bind)")
	rootCmd.Flags().IntVar(&serfAdvertisePort, "serf-advertise-port", 0, "Serf advertise port (if different from bind)")
	rootCmd.Flags().StringVar(&raftAddr, "raft-addr", "127.0.0.1:29020", "Raft bind address (host:port)")
	rootCmd.Flags().StringVar(&raftDir, "data", "./data/raft", "Data directory for Raft log")
	rootCmd.Flags().StringVar(&joinAddr, "join", "", "Join existing cluster peer (host:port)")
	rootCmd.Flags().StringVar(&apiAddr, "api-addr", "127.0.0.1:29000", "API address")

	// Security Flags
	rootCmd.Flags().BoolVar(&productionMode, "production", false, "Restrict agent discovery to binaries with verified signatures")
	rootCmd.Flags().StringVar(&logLevel, "log-level", "info", "Log level: debug, info, warn, error")
	rootCmd.Flags().StringVar(&logFormat, "log-format", "", "Log format: json or console (default: json in production, console otherwise)")
	rootCmd.Flags().StringVar(&logFile, "log-file", "", "Also write logs to this file (rotated)")
	rootCmd.Flags().StringVar(&logLokiURL, "log-loki-url", "", "Forward logs to this Loki endpoint")
	rootCmd.Flags().StringVar(&agentVerifyKey, "agent-verify-key", "", "Path to the Ed25519 public key for agent signature verification (falls back to $RUNTIME_VERIFY_KEY)")
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
