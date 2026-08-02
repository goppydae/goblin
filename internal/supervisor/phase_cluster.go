package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/serf/serf"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/logattr"
)

// phaseCluster runs Phase 4: this node becomes a cluster member. Serf
// gossip and Raft consensus attach to the listener Phase 1 already
// bound - no new listeners are opened - and the distributed event bus
// is bridged to Serf's event stream.
//
// The caller owns shutting down st.membership and st.consensus; both
// are live on return.
func (s *Supervisor) phaseCluster(ctx context.Context, st *runState, failFatal func(error)) error {
	if err := s.joinGossip(ctx, st); err != nil {
		return err
	}
	if err := s.startConsensus(ctx, st, failFatal); err != nil {
		return err
	}
	s.bridgeSerfEvents(st)
	return nil
}

// joinGossip registers the serf plane and brings up membership.
func (s *Supervisor) joinGossip(ctx context.Context, st *runState) error {
	// Serf rides the shared listener: routed serf-quic connections in,
	// per-peer dials out.
	serfConns, err := st.sharedLn.Register(transport.ALPNSerfQUIC)
	if err != nil {
		return fmt.Errorf("register serf plane: %w", err)
	}
	serfTransport, err := transport.NewRoutedQUICSerfTransport(serfConns, st.advertiseUDP, st.tlsCfg)
	if err != nil {
		return fmt.Errorf("failed to create QUIC serf transport: %w", err)
	}

	membership, err := cluster.NewMembership(st.nodeID, st.listenHost, st.listenPort,
		st.advertiseUDP.IP.String(), st.advertiseUDP.Port, st.tags, st.secretKey, serfTransport)
	if err != nil {
		err = fmt.Errorf("failed to create membership: %w", err)
		if serr := serfTransport.Shutdown(); serr != nil { // Cleanup on failure
			return errors.Join(err, serr)
		}
		return err
	}
	st.membership = membership

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "serf membership initialized", logattr.Addr(st.advertiseUDP.String()))

	if s.cfg.JoinAddr != "" {
		if jerr := membership.Join([]string{s.cfg.JoinAddr}); jerr != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to join cluster", logattr.Addr(s.cfg.JoinAddr), logattr.Err(jerr))
		} else {
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "joined cluster", logattr.Addr(s.cfg.JoinAddr))
		}
	}
	return nil
}

// startConsensus registers the raft plane, brings up the engine, and
// starts the loops that seed and maintain cluster-wide state.
func (s *Supervisor) startConsensus(ctx context.Context, st *runState, failFatal func(error)) error {
	raftConns, err := st.sharedLn.Register(transport.ALPNRaftQUIC)
	if err != nil {
		return fmt.Errorf("register raft plane: %w", err)
	}
	raftStream := transport.NewRoutedQUICStreamLayer(raftConns, st.advertiseUDP, st.tlsCfg)

	// With bootstrap-expect the initial configuration is not knowable at
	// construction time - the engine comes up unseeded and is
	// bootstrapped once gossip shows the whole seed set.
	seedAlone := s.cfg.JoinAddr == "" && s.cfg.BootstrapExpect < 2

	// Loaded BEFORE the engine, because the engine builds the FSM and the
	// FSM needs this node's root of trust at construction: raft may call
	// Restore as soon as it owns the FSM, and a snapshot that arrives
	// before the anchor is installed is a snapshot this node cannot
	// authenticate (GOBLIN-DIV-047). These keys are also still proposed as
	// a seed below - that is the replicated half; this is the local half,
	// and they are deliberately the same material.
	operatorKeys, err := loadOperatorKeys(s.cfg.OperatorKeyFiles)
	if err != nil {
		return fmt.Errorf("load operator keys: %w", err)
	}

	engine, err := consensus.NewConsensus(st.nodeID, s.cfg.RaftDir, raftStream, seedAlone,
		s.cfg.RaftSnapshotThreshold, s.cfg.RaftSnapshotInterval, s.cfg.RaftTrailingLogs,
		operatorKeys)
	if err != nil {
		return fmt.Errorf("failed to create consensus: %w", err)
	}
	st.consensus = engine

	// GOBLIN-DIV-049: nothing else retires a migration record left by a
	// leader that died mid-move, and the reconciler now honours those
	// records - so without this, one dead coordinator makes an instance
	// permanently unrecoverable.
	s.loops.spawn(tierRun, "migration-sweeper", func() {
		runMigrationSweeper(ctx, engine, slog.Default(), time.Second)
	})

	s.loops.spawn(tierRun, "operator-key-seeder", func() {
		serr := runOperatorKeySeeder(ctx, engine, operatorKeys, slog.Default(), time.Second)
		if serr == nil || errors.Is(serr, context.Canceled) {
			return // seeded, or shutting down
		}
		// Never fatal. See ErrOperatorConfigStale for why: the flag is
		// inert on an already-seeded cluster, and the fatal-versus-benign
		// distinction is not reliably knowable from here. The
		// goblin_operator_key_config_drift gauge is what an operator
		// alerts on; this line is for whoever is reading the log at the
		// time.
		slog.Default().LogAttrs(ctx, slog.LevelError,
			"some or all of this node's configured operator keys are absent from the cluster registry; those keys authorize nothing and re-supplying them via --operator-key is inert",
			logattr.Err(serr))
	})

	if s.cfg.BootstrapExpect > 1 {
		// In the background: the node serves gossip and RPC while it
		// waits: it is running, the cluster simply is not seeded yet.
		// Blocking here would make an incomplete seed set look like a
		// dead daemon.
		s.loops.spawn(tierRun, "bootstrap-expect", func() {
			berr := runBootstrapExpect(ctx, s.cfg.BootstrapExpect, s.cfg.JoinAddr, st.membership, engine)
			if berr == nil {
				return
			}
			if errors.Is(berr, context.Canceled) {
				return // shutting down
			}
			// A disagreement about the seed set would split the cluster;
			// refuse to run rather than seed half of it.
			slog.Default().LogAttrs(ctx, slog.LevelError, "bootstrap-expect failed", logattr.Err(berr))
			failFatal(berr)
		})
	}

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "raft consensus initialized", logattr.Addr(st.advertiseUDP.String()))

	st.bus = eventbus.NewDistributedEventBus(st.nodeID, st.membership, engine)
	return nil
}

// bridgeSerfEvents fans Serf's single event handler out to the
// distributed bus and to raft voter admission. Serf supports one
// handler, so the supervisor is the fan-out point.
func (s *Supervisor) bridgeSerfEvents(st *runState) {
	nodeID, engine, bus := st.nodeID, st.consensus, st.bus

	st.membership.SetEventHandler(func(e serf.Event) {
		// The distributed bus ingests goblin.event user events here.
		bus.HandleSerfEvent(e)
		ev, ok := e.(serf.MemberEvent)
		if !ok {
			return
		}
		for _, member := range ev.Members {
			var topic string
			switch ev.EventType() {
			case serf.EventMemberJoin:
				topic = "cluster.node.joined"
				// Every node races addVoterWithRetry for every join:
				// only the (eventual) leader's attempt lands, and the
				// rest expire quietly with ErrNotAdmitting. Gating on
				// instantaneous leadership here is the bug that left
				// three independent single-node rafts (R6 + 2b e2e).
				if member.Name != nodeID {
					// Single-listener model: the member's advertised
					// serf address IS its raft address (and its RPC
					// address) - no per-protocol tags exist.
					peerAddr := net.JoinHostPort(member.Addr.String(), strconv.Itoa(int(member.Port)))
					slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "adding raft voter", logattr.Member(member.Name), logattr.Addr(peerAddr))
					// Not tracked by the loopGroup: this is per-EVENT
					// work with its own retry bound, not a long-lived
					// loop. It is in the same unjoined set as the
					// per-connection handlers (GOBLIN-DIV-038 residual).
					go func(name, addr string) {
						err := addVoterWithRetry(context.Background(), engine, name, addr)
						switch {
						case err == nil:
						case errors.Is(err, ErrNotAdmitting):
							slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "voter admission left to the leader", logattr.Member(name))
						default:
							slog.Default().LogAttrs(context.Background(), slog.LevelError, "failed to add voter", logattr.Member(name), logattr.Err(err))
						}
					}(member.Name, peerAddr)
				}
			case serf.EventMemberLeave:
				topic = "cluster.node.left"
			case serf.EventMemberFailed:
				topic = "cluster.node.failed"
			case serf.EventMemberUpdate:
				topic = "cluster.node.updated"
			case serf.EventMemberReap:
				topic = "cluster.node.reaped"
			}

			if topic == "" {
				continue
			}
			payload := map[string]interface{}{
				"name":   member.Name,
				"addr":   member.Addr.String(),
				"port":   member.Port,
				"status": member.Status.String(),
				"tags":   member.Tags,
			}
			// Serf event handler is a callback terminus: there is no
			// caller to propagate to, so a publish failure is logged.
			if err := bus.PublishLocal("system", topic, payload, []string{"cluster", member.Name}); err != nil {
				slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "publish failed", logattr.Topic(topic), logattr.Member(member.Name), logattr.Err(err))
			}
		}
	})
}
