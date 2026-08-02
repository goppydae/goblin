package supervisor

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapiconfig "github.com/goppydae/gapi/core/config"
	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
	gapilogging "github.com/goppydae/gapi/core/logging"
	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"
	"github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/logattr"
)

// Config holds configuration for the Supervisor
type Config struct {
	NodeID string
	// ListenAddr is the single control-plane bind address: every
	// protocol (gapi-quic, goblin-rpc, serf-quic, raft-quic) shares it,
	// routed by ALPN (GOBLIN-DIV-023). Metrics is the only other port.
	ListenAddr    string
	AdvertiseAddr string
	AdvertisePort int
	RaftDir       string
	// RaftSnapshotThreshold overrides how many outstanding Raft log
	// entries trigger a compaction snapshot (raft.DefaultConfig: 8192).
	// 0 keeps the Raft default; operators tune it down on write-heavy
	// clusters to bound the trailing log's disk footprint and how much a
	// late-joining node must replay before it is caught up.
	RaftSnapshotThreshold uint64
	// RaftSnapshotInterval overrides how often Raft checks whether a
	// compaction snapshot is due (raft.DefaultConfig: 120s). 0 keeps the
	// Raft default.
	RaftSnapshotInterval time.Duration
	// RaftTrailingLogs overrides how many log entries Raft retains after
	// a snapshot for fast follower replay instead of a full snapshot
	// transfer (raft.DefaultConfig: 10240). 0 keeps the Raft default.
	RaftTrailingLogs uint64
	JoinAddr         string
	// BootstrapExpect is the number of seed nodes that must be visible
	// through gossip before the cluster seeds itself. Every seed is
	// configured with the same number and they elect one bootstrapper
	// among themselves, so no node has to be designated by hand. 0 (or
	// 1) keeps the seed model: whichever node has no JoinAddr
	// bootstraps alone.
	BootstrapExpect int
	Tags            map[string]string
	EncryptionKey   string // Base64 encoded 32-byte key
	CertFile        string
	KeyFile         string
	CAFile          string
	MetricsAddr     string
	// ProductionMode restricts embedded-GAPI agent discovery to binaries
	// with verified signatures (review R20).
	ProductionMode bool
	// AgentVerifyKey is the path to the Ed25519 public key that verifies
	// agent-binary signatures; falls back to $GOBLIN_VERIFY_KEY.
	AgentVerifyKey string
	// OperatorKeyFiles are paths to hex-encoded Ed25519 public keys that
	// bootstrap the cluster's operator registry (GOBLIN-DIV-015 piece 1).
	// They are this node's claim about the root of trust; they become
	// authoritative only once committed to Raft. Empty means the cluster
	// refuses every mutating verb.
	OperatorKeyFiles []string
	// Logging mirrors gapi's logging configuration (level, format, file
	// rotation, Loki); handlers are built by the kernel's core/logging.
	Logging gapiconfig.LoggingConfig
	// NetworkGateTimeout bounds the network-readiness phase gate: when
	// nonzero, Run blocks until the kernel's agent.network.running topic
	// fires on the local bus, failing loudly on expiry (GOBLIN-DIV-011,
	// R13). Zero disables the gate - the topic has no producer unless a
	// network agent is deployed.
	NetworkGateTimeout time.Duration
	// Pid1Mode activates the embedded kernel's Phase 0 pre-userspace
	// boot before any cluster code, and the reversed teardown on
	// shutdown (goblin-architecture.md). goblind IS the init process.
	Pid1Mode         bool
	NoEarlyMounts    bool
	WatchdogDevice   string
	WatchdogInterval time.Duration
	ShutdownGrace    time.Duration
}

// Supervisor manages the Goblin daemon components
type Supervisor struct {
	cfg      Config
	agentMgr *gapiagentmgr.AgentManager // GAPI agent manager (optional)

	// issuer mints capability tokens under this boot's keypair;
	// revocations is the gossip-merged revocation Bloom filter.
	issuer      *capability.Issuer
	revocations *capability.Revocations

	// loops tracks every long-lived goroutine the supervisor starts so
	// shutdown joins them instead of racing them (GOBLIN-DIV-038). It is
	// built in New, not in Run, because enablePid1 spawns into it before
	// Phase 1 exists - and because a test needs a seam to add a probe
	// loop before Run is called.
	loops *loopGroup
}

// New creates a new Supervisor
func New(cfg Config) *Supervisor {
	return &Supervisor{cfg: cfg, loops: newLoopGroup()}
}

// runState carries what each boot phase produces to the phases that
// need it. It is the explicit form of what used to be thirty locals in
// one 600-line function: a phase now declares its inputs by reading
// them here rather than by closing over whatever happened to be in
// scope.
//
// That explicitness is load-bearing beyond readability. A later target
// model orders units by their declared dependencies, and it cannot
// order what it cannot see.
type runState struct {
	// Phase 1 - identity and transport
	nodeID       string
	secretKey    []byte
	tlsCfg       *tls.Config
	listenHost   string
	listenPort   int
	advertiseUDP *net.UDPAddr
	tags         map[string]string
	sharedLn     *transport.SharedListener

	// Phase 1 - local runtime
	localBus     *gapieventbus.EventBus[*anypb.Any]
	lifecycleBus *gapilifecycle.TypedBus
	agentMgr     *gapiagentmgr.AgentManager

	// Phase 4 - cluster
	membership *cluster.Membership
	consensus  *consensus.Consensus
	bus        *eventbus.DistributedEventBus

	// Phase 5 - serving
	kvStore      *store.Store
	sched        *scheduler.Scheduler
	schedulerRPC *SchedulerRPC
	tracker      *instanceTracker
}

// Run boots the node through its phases and blocks until the context is
// cancelled, then tears down in a bounded, ordered sequence.
//
// The phase order here IS the boot contract. It differs from what the
// architecture doc described before this change: the local runtime and
// the network-readiness gate now precede cluster join, rather than
// following it. Phase 2 (local agent start) is absent - see
// GOBLIN-DIV-050 - so the gate has no producer and a nonzero
// NetworkGateTimeout always expires.
//
// There is exactly ONE exit path. boot returns on the first phase
// error or on cancellation; teardown then runs unconditionally, in one
// order, for both cases. Resource shutdowns are deliberately NOT
// defers: a defer registered when a resource is created runs in
// creation order, which puts consensus and membership teardown BEFORE
// the loops that touch them have been joined. That inversion is the
// defect GOBLIN-DIV-038 names, and it cannot be fixed by reordering
// defers because the join point has to outlive every one of them.
func (s *Supervisor) Run(ctx context.Context) (err error) {
	// Logging first: everything after this logs through slog.
	logger, logCloser, err := gapilogging.Build(&s.cfg.Logging)
	if err != nil {
		return fmt.Errorf("init logging: %w", err)
	}
	defer func() {
		if cerr := logCloser.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close log sink: %w", cerr)
		}
	}()
	slog.SetDefault(logger)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Why the context was cancelled, so a fatal misconfiguration exits
	// nonzero instead of looking like a clean stop. Buffered and
	// select-guarded so the first cause wins and no failure path can
	// block on it.
	fatalCh := make(chan error, 1)
	failFatal := func(cause error) {
		select {
		case fatalCh <- cause:
		default:
		}
		runCancel()
	}

	// Phase 0 (PID-1 mode): the embedded kernel's pre-userspace
	// obligations run before any cluster code - a hung network layer
	// can never block init duties.
	var pid1 *pid1Completion
	if s.cfg.Pid1Mode {
		p, perr := s.enablePid1(runCtx, runCancel)
		if perr != nil {
			return fmt.Errorf("phase 0: %w", perr)
		}
		pid1 = p
	}

	st := &runState{}
	bootErr := s.boot(runCtx, st, failFatal)

	// Cancel BEFORE joining: on a phase error the context is still
	// live, so without this the loops already started would never see a
	// stop and the join would burn the whole grace period.
	runCancel()
	tdErr := s.teardown(st, pid1)

	err = bootErr
	if err == nil {
		err = tdErr
	}
	// The fatal cause must beat whatever path actually fired: the phase
	// returns are ctx-aware and surface "context canceled" the moment
	// failFatal cancels, which reports the symptom instead of the cause.
	select {
	case ferr := <-fatalCh:
		err = ferr
	default:
	}
	return err
}

// boot runs the phases in order and blocks until the context ends.
// It returns the first phase error, or nil on a clean cancellation.
func (s *Supervisor) boot(ctx context.Context, st *runState, failFatal func(error)) error {
	// The phases are TARGETS REACHED (GOBLIN-DIV-050). Each phase does
	// its own work and then starts the agents that declared themselves
	// wanted by the state that work establishes, so Phase 2 is not a step
	// between the others - it is distributed across all of them, which is
	// what the entry meant by a target model MOVING Phase 2 rather than
	// filtering it.
	//
	// Phase 1: local runtime.
	if err := s.phaseLocal(ctx, st); err != nil {
		return err
	}
	// Phase 2: local agents. Reached here because the local runtime is up
	// and the network is not required.
	if err := s.reachTarget(ctx, st, TargetLocal, applyStart); err != nil {
		return err
	}
	s.warnUnreachableTargets(ctx, st)

	// Phase 3: network readiness. local.target above is what gives this
	// gate a producer - before it, nothing on this node ever published
	// the readiness topic, so a nonzero timeout always expired.
	if err := s.phaseNetworkGate(ctx, st); err != nil {
		return err
	}
	if err := s.reachTarget(ctx, st, TargetNetworkReady, applyStart); err != nil {
		return err
	}

	// Phase 4: cluster join.
	if err := s.phaseCluster(ctx, st, failFatal); err != nil {
		return err
	}
	if err := s.reachTarget(ctx, st, TargetCluster, applyStart); err != nil {
		return err
	}

	// Phase 5: serving.
	if err := s.phaseServing(ctx, st); err != nil {
		return err
	}
	if err := s.reachTarget(ctx, st, TargetDistributed, applyStart); err != nil {
		return err
	}

	<-ctx.Done()
	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "shutting down")
	return nil
}

// teardown runs the ordered shutdown. It is called on every exit path,
// including a phase error, so every field it touches is nil-guarded:
// a boot that failed in Phase 1 has no membership to shut down.
//
// The order is the point:
//
//	join tier 1 -> drain -> serf leave -> raft -> membership ->
//	listener -> local teardown -> join tier 0
//
// Joining first is what guarantees no loop touches consensus,
// membership or the listener after it is gone.
func (s *Supervisor) teardown(st *runState, pid1 *pid1Completion) error {
	grace := s.shutdownGrace()
	s.loops.joinTier(tierRun, "run", grace)

	var errs []error

	if pid1 != nil && st.sched != nil {
		// Reversed teardown: drain local jobs to peers (bounded) before
		// leaving. The drain deadline derives from Background, not the
		// already-cancelled run context (Go manifesto section 11).
		drainCtx, cancel := context.WithTimeout(context.Background(), pid1.grace)
		if _, derr := st.sched.DrainNode(drainCtx, st.nodeID); derr != nil {
			slog.Default().LogAttrs(drainCtx, slog.LevelWarn, "drain during shutdown failed", logattr.Err(derr))
		}
		cancel()
	}
	if pid1 != nil && st.membership != nil {
		if lerr := st.membership.Leave(); lerr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "serf leave during shutdown failed", logattr.Err(lerr))
		}
	}
	if st.consensus != nil {
		if cerr := st.consensus.Shutdown(); cerr != nil {
			errs = append(errs, fmt.Errorf("shutdown consensus: %w", cerr))
		}
	}
	if st.membership != nil {
		if serr := st.membership.Shutdown(); serr != nil {
			errs = append(errs, fmt.Errorf("shutdown membership: %w", serr))
		}
	}
	if st.sharedLn != nil {
		if cerr := st.sharedLn.Close(); cerr != nil {
			errs = append(errs, fmt.Errorf("close control-plane listener: %w", cerr))
		}
	}
	if pid1 != nil && s.agentMgr != nil {
		// Hands StopAll -> sync -> umount -> reboot to the kernel
		// executor. Rootless containers cannot reboot; the executor
		// falls through to exit, which IS container-init poweroff.
		pid1.complete(context.Background(), s.agentMgr)
	}

	// Tier 0 joins LAST: the reaper must still be reaping while
	// StopAll kills children, and the watchdog must keep petting until
	// reboot(2) fires. Reachable only when the reboot did not happen -
	// when it lands, nothing is waiting on anything.
	s.loops.joinTier(tierPreUserspace, "pre-userspace", grace)

	return errors.Join(errs...)
}

// decodeRevocationFilter pulls the Bloom filter bytes out of a bus
// payload, which carries them as raw bytes locally and as base64 once
// they have been through the JSON gossip encoding.
func decodeRevocationFilter(ctx context.Context, v interface{}) ([]byte, bool) {
	switch f := v.(type) {
	case []byte:
		return f, true
	case string:
		decoded, err := base64.StdEncoding.DecodeString(f)
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "undecodable revocation snapshot", logattr.Err(err))
			return nil, false
		}
		return decoded, true
	default:
		return nil, false
	}
}
