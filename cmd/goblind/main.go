package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"

	gapiconfig "github.com/goppydae/gapi/core/config"
	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/cli"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/goppydae/goblin/internal/supervisor"
	"github.com/spf13/cobra"
)

// newRootCmd builds goblind's root. The flag structs are what
// NewGoblindRoot RETURNS and the start action needs both, so they are
// declared before construction and captured by the closure, which
// resolves them when start runs - always after the assignment below.
// Same shape as gapid's (GAPI-DIV-058).
func newRootCmd() (*cobra.Command, *gapicli.DaemonFlags, *cli.GoblindStartFlags) {
	var daemonFlags *gapicli.DaemonFlags
	var startFlags *cli.GoblindStartFlags

	root, d, sf := cli.NewGoblindRoot(func(cmd *cobra.Command, args []string) error {
		return runDaemon(cmd.Context(), daemonFlags, startFlags)
	})
	daemonFlags, startFlags = d, sf
	return root, d, sf
}

// runDaemon is the body of `goblind start`.
func runDaemon(ctx context.Context, flags *gapicli.DaemonFlags, sf *cli.GoblindStartFlags) error {
	// Detect Resources. Logged inside Run, after the configured
	// handler is installed, so the line honors --log-format.
	cpu := runtime.NumCPU()
	mem := getTotalMemory()

	tags := map[string]string{
		"cpu":    fmt.Sprintf("%d", cpu),
		"memory": fmt.Sprintf("%d", mem),
	}

	cfg := supervisor.Config{
		NodeID:                flags.ID,
		ListenAddr:            sf.ListenAddr,
		AdvertiseAddr:         sf.AdvertiseAddr,
		RaftDir:               sf.RaftDir,
		RaftSnapshotThreshold: sf.RaftSnapshotThreshold,
		RaftSnapshotInterval:  sf.RaftSnapshotInterval,
		RaftTrailingLogs:      sf.RaftTrailingLogs,
		JoinAddr:              sf.JoinAddr,
		BootstrapExpect:       sf.BootstrapExpect,
		Pid1Mode:              sf.Pid1Mode,
		NoEarlyMounts:         sf.NoEarlyMounts,
		WatchdogDevice:        sf.WatchdogDevice,
		WatchdogInterval:      sf.WatchdogInterval,
		ShutdownGrace:         sf.ShutdownGrace,
		Tags:                  tags,
		EncryptionKey:         sf.EncryptionKey,
		CertFile:              flags.TLSCert,
		KeyFile:               flags.TLSKey,
		CAFile:                flags.TLSCA,
		MetricsAddr:           flags.MetricsAddr,
		ProductionMode:        sf.ProductionMode,
		AgentVerifyKey:        sf.AgentVerifyKey,
		OperatorKeyFiles:      sf.OperatorKeyFiles,
		Logging:               buildLoggingConfig(flags, sf.ProductionMode),
		NetworkGateTimeout:    sf.NetworkGateTimeout,
	}
	return supervisor.New(cfg).Run(ctx)
}

// buildLoggingConfig mirrors gapi's LoggingConfig from goblind's flags.
// Production defaults to JSON so cluster logs are machine-parseable.
//
// --log-level's default moved from "info" to the shared registrar's "",
// which is behaviour-preserving: logging.ParseLevel maps "" to Info.
func buildLoggingConfig(flags *gapicli.DaemonFlags, production bool) gapiconfig.LoggingConfig {
	cfg := gapiconfig.LoggingConfig{Level: flags.LogLevel, Format: flags.LogFormat}
	if cfg.Format == "" {
		if production {
			cfg.Format = "json"
		} else {
			cfg.Format = "console"
		}
	}
	if flags.LogFile != "" {
		cfg.File = gapiconfig.FileOutputConfig{
			Enabled:    true,
			Path:       flags.LogFile,
			MaxSize:    100, // megabytes
			MaxBackups: 5,
			MaxAge:     30, // days
			Compress:   true,
		}
	}
	if flags.LogLokiURL != "" {
		cfg.Loki = gapiconfig.LokiOutputConfig{Enabled: true, URL: flags.LogLokiURL}
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

func main() {
	root, _, _ := newRootCmd()
	// RunRoot, not Execute: a bare `goblind` must print help and exit
	// NON-ZERO, which cobra cannot do on its own for a root with no RunE
	// (cli-contract.md, GOBLIN-DIV-053).
	if err := gapicli.RunRoot(root, os.Args[1:]); err != nil {
		if !errors.Is(err, gapicli.ErrNoCommand) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
