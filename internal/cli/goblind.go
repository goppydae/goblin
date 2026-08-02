package cli

import (
	"time"

	"github.com/spf13/cobra"

	gapicli "github.com/goppydae/gapi/pkg/cli"
	"github.com/goppydae/goblin/internal/version"
)

// This file builds goblind's root. It lives beside goblinctl's rather
// than in package main for the same reason gapid's does (GAPI-DIV-058):
// a root declared inside main is unreachable from any test, and the
// contract's parity check compares constructed roots.
//
// The shape comes from the kernel. gapicli.NewDaemonRoot supplies the
// root with NO RunE, the identity surface, and the nine persistent flags
// that describe the process; this file adds only what configures a run.

// GoblindStartFlags are local to `goblind start`. They configure a run
// rather than describing the process, which is what keeps them off the
// persistent set (cli-contract.md).
//
// The split is 9 persistent to 18 local. GOBLIN-DIV-053 states 25 flags
// splitting 9 to 16; the root actually carried 27. Counted rather than
// copied, because an entry that names a count rots.
type GoblindStartFlags struct {
	// Cluster membership and control plane.
	ListenAddr      string
	AdvertiseAddr   string
	JoinAddr        string
	BootstrapExpect int
	EncryptionKey   string

	// Raft and storage.
	RaftDir               string
	RaftSnapshotInterval  time.Duration
	RaftSnapshotThreshold uint64
	RaftTrailingLogs      uint64

	// Security.
	AgentVerifyKey   string
	ProductionMode   bool
	OperatorKeyFiles []string

	// Boot and lifecycle.
	Pid1Mode           bool
	NoEarlyMounts      bool
	NetworkGateTimeout time.Duration
	ShutdownGrace      time.Duration
	WatchdogDevice     string
	WatchdogInterval   time.Duration
}

// NewGoblindRoot builds goblind's root and its `start` subcommand.
//
// start is a parameter so this package never imports internal/supervisor
// - the same constraint the kernel's pkg/cli holds, and what makes the
// root constructible by a test that must not boot a node.
func NewGoblindRoot(start func(*cobra.Command, []string) error) (*cobra.Command, *gapicli.DaemonFlags, *GoblindStartFlags) {
	// "goblin" is the PRODUCT, not the binary. Every host-namespaced
	// resource the embedded kernel touches derives from it: GOBLIN_
	// variables, /etc/goblin, /usr/lib/goblin/agents, goblind-<id>
	// cgroups, and the "goblind:" tag an operator reads in dmesg under
	// --pid1. Declaring it here is what makes the kernel invisible to a
	// goblind operator (GOBLIN-DIV-055).
	root, daemonFlags := gapicli.NewDaemonRoot(
		"goblin", "goblind", version.Version, "Goblin Distributed Supervisor Daemon", start)

	sf := &GoblindStartFlags{}
	startCmd, _, err := root.Find([]string{"start"})
	if err != nil {
		// NewDaemonRoot always adds `start`; a miss means the kernel's
		// constructor changed shape and every caller is broken, not just
		// this one.
		panic("goblind: start subcommand missing from daemon root: " + err.Error())
	}
	f := startCmd.Flags()

	// Cluster membership and control plane.
	f.StringVar(&sf.ListenAddr, "listen-addr", "127.0.0.1:29000",
		"Single control-plane bind address (QUIC; carries agent events, RPC, Serf, and Raft via ALPN)")
	f.StringVar(&sf.AdvertiseAddr, "advertise-addr", "", "Advertise address (if different from bind)")
	f.StringVar(&sf.JoinAddr, "join", "", "Join existing cluster peer (host:port)")
	f.IntVar(&sf.BootstrapExpect, "bootstrap-expect", 0,
		"Seed the cluster once this many nodes carrying the same value are visible; one of them is elected to bootstrap (0: seed model, the node with no --join bootstraps alone)")
	f.StringVar(&sf.EncryptionKey, "encrypt", "", "Base64 encoded 32-byte secret key for Serf encryption")

	// Raft and storage.
	f.StringVar(&sf.RaftDir, "data", "./data/raft", "Data directory for Raft log")
	f.DurationVar(&sf.RaftSnapshotInterval, "raft-snapshot-interval", 0,
		"How often Raft checks whether a compaction snapshot is due (0: raft default, 120s)")
	f.Uint64Var(&sf.RaftSnapshotThreshold, "raft-snapshot-threshold", 0,
		"Outstanding Raft log entries before a compaction snapshot (0: raft default, 8192)")
	f.Uint64Var(&sf.RaftTrailingLogs, "raft-trailing-logs", 0,
		"Raft log entries retained after a snapshot for fast follower replay (0: raft default, 10240)")

	// Security.
	f.StringVar(&sf.AgentVerifyKey, "agent-verify-key", "",
		"Path to the Ed25519 public key for agent signature verification (falls back to $GOBLIN_VERIFY_KEY)")
	f.BoolVar(&sf.ProductionMode, "production", false,
		"Restrict agent discovery to binaries with verified signatures")
	f.StringArrayVar(&sf.OperatorKeyFiles, "operator-key", nil,
		"Path to a hex-encoded Ed25519 operator public key that bootstraps the cluster's operator registry (repeatable; with none, every mutating verb is refused). Keep the matching private key: the last registered key cannot be removed, so a registry left holding only keys nobody controls can neither authorize anything nor be re-seeded, and the only recovery is wiping the data dir on EVERY replica and re-bootstrapping. Produce keys with 'goblinctl operator keygen'. These are also this node's FOUNDING SEED: a snapshot's registry is authenticated by replaying its provenance from exactly this key set, so every node in a cluster must be given the same one, and a node joining after the registry has been rotated is still given the FOUNDING keys rather than the current ones. Keep them for the cluster's life (GOBLIN-DIV-047).")

	// Boot and lifecycle.
	f.BoolVar(&sf.Pid1Mode, "pid1", false,
		"Run as PID 1: kernel Phase 0 boot before the cluster stack; reversed teardown on shutdown")
	f.BoolVar(&sf.NoEarlyMounts, "no-early-mounts", false,
		"Skip the Phase 0 mount table (container environments)")
	f.DurationVar(&sf.NetworkGateTimeout, "network-gate-timeout", 0,
		"Block startup until the network agent reports running, failing after this bound (0: gate disabled)")
	f.DurationVar(&sf.ShutdownGrace, "shutdown-grace", 10*time.Second,
		"Per-phase shutdown grace (drain + agent stop) before forcing")
	f.StringVar(&sf.WatchdogDevice, "watchdog-device", "",
		"Hardware watchdog device to keep alive (empty: disabled)")
	f.DurationVar(&sf.WatchdogInterval, "watchdog-interval", 10*time.Second,
		"Watchdog keepalive kick interval")

	return root, daemonFlags, sf
}
