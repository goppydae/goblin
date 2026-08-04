// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/serf/serf"
	"google.golang.org/protobuf/types/known/anypb"

	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/procsig"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/core/migration"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/core/store"
	"github.com/goppydae/goblin/core/transport"
	"github.com/goppydae/goblin/internal/hlc"
	"github.com/goppydae/goblin/internal/logattr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// phaseServing runs Phase 5: the node becomes useful to the cluster.
// Scheduler, RPC surfaces, the bus subscriptions that feed them, and
// every serving loop.
func (s *Supervisor) phaseServing(ctx context.Context, st *runState) error {
	s.startScheduler(ctx, st)
	if err := s.startRPC(ctx, st); err != nil {
		return err
	}
	if err := s.startTelemetry(ctx, st); err != nil {
		return err
	}
	s.startMonitors(ctx, st)
	return nil
}

// startScheduler builds the KV store and the scheduler over consensus.
func (s *Supervisor) startScheduler(ctx context.Context, st *runState) {
	st.kvStore = store.NewStore(st.consensus, st.bus)
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "distributed kv store initialized")

	tlsCfg := st.tlsCfg
	st.sched = scheduler.NewScheduler(st.kvStore, st.membership, st.bus, st.consensus.IsLeader,
		func(addr string) (scheduler.RPCClient, error) {
			// addr is the target member's single advertised address; the
			// dial's goblin-rpc ALPN selects the plane.
			return NewQUICRPCClient(addr, tlsCfg)
		})
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler initialized")
}

// startRPC builds the RPC surfaces, registers their handlers, and
// attaches the planes that serve them to the shared listener.
func (s *Supervisor) startRPC(ctx context.Context, st *runState) error {
	// Migration collaborators. The dialer is the same one the scheduler
	// uses - one place decides how a node is reached - and the resolver
	// is membership's view, so the coordinator cannot disagree with the
	// reconciler about where a node lives.
	tlsCfg := st.tlsCfg
	migrateNodes := migration.NewRPCNodes(
		func(addr string) (migration.Caller, error) {
			return NewQUICRPCClient(addr, tlsCfg)
		},
		st.sched.NodeAddress,
	)

	schedulerRPC := &SchedulerRPC{
		scheduler:   st.sched,
		membership:  st.membership,
		consensus:   st.consensus,
		agentMgr:    st.agentMgr,
		issuer:      s.issuer,
		revocations: s.revocations,
		members:     st.membership,

		migrateNodes:  migrateNodes,
		migrateRaft:   migration.NewRaftProposer(st.consensus, 0),
		migrateLogger: slog.Default(),

		// The send half of revocation gossip. The subscriber below is
		// the receive half and predates this by weeks; nothing filled
		// this side, so the topic carried no traffic at all.
		publishRevocation: func(tokenID []byte) {
			// PublishCluster, not PublishLocal: a revocation is only
			// useful on the nodes that will be asked to honour the
			// token, which is every node but this one.
			if err := st.bus.PublishCluster("capability", "capability.revocation",
				map[string]interface{}{"token_id": tokenID}, nil); err != nil {
				slog.Default().LogAttrs(ctx, slog.LevelWarn,
					"revocation broadcast failed; the token is refused locally only",
					logattr.Err(err))
			}
		},
	}
	st.schedulerRPC = schedulerRPC
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler rpc created")

	quicServer := NewQUICRPCServer()
	RegisterSchedulerHandlers(quicServer, schedulerRPC)

	instTracker := newInstanceTracker()
	st.tracker = instTracker
	// Checkpoint images live beside the Raft data rather than under a
	// separate root: both are node-local durable state with the same
	// lifetime, and an operator who relocates one means to relocate the
	// other. Sibling, not nested - wiping raft state must not discard
	// images that a migration in flight still needs.
	imageRoot := filepath.Join(filepath.Dir(s.cfg.RaftDir), "checkpoints")
	// Client TLS for dialing a peer's goblin-ckpt listener.
	//
	// This is the node's OWN tls.Config, not one synthesized from
	// CAFile. The two-node test proved why: building it from CAFile
	// alone produced a config that verified against the system roots,
	// so every dial failed with "certificate signed by unknown
	// authority" against the cluster's self-signed certs, while every
	// other plane worked because they all share tlsCfg. One config, one
	// verification policy, for every plane on the shared listener.
	//
	// DialAndFetch clones it before stamping the ALPN, so this stays
	// usable by the other planes.
	nodeRPC := &NodeRPC{
		agentMgr:  st.agentMgr,
		tracker:   instTracker,
		images:    migration.NewStore(imageRoot),
		ckptTLS:   tlsCfg,
		consensus: st.consensus,
	}
	RegisterNodeHandlers(quicServer, nodeRPC)
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "scheduler and node rpc handlers registered")

	// RPC and embedded-kernel planes ride the shared listener: the ALPN
	// router hands each plane its own connection stream.
	rpcConns, err := st.sharedLn.Register(transport.ALPNGoblinRPC)
	if err != nil {
		return fmt.Errorf("register rpc plane: %w", err)
	}
	gapiConns, err := st.sharedLn.Register(transport.ALPNGapiQUIC)
	if err != nil {
		return fmt.Errorf("register agent event plane: %w", err)
	}
	// Checkpoint image transfer. Registering the ALPN in the registry
	// only advertises it; without an adapter the router refuses the
	// connection with CodeALPNNotServing, which is how a migration
	// failed with "ALPN goblin-ckpt not serving" while every other
	// plane worked.
	ckptConns, err := st.sharedLn.Register(transport.ALPNGoblinCkpt)
	if err != nil {
		return fmt.Errorf("register checkpoint plane: %w", err)
	}

	ckptServer := migration.NewServer(
		nodeRPC.Images(),
		checkpointAuthorizer(schedulerRPC.capabilityKeyResolver(), s.revocations, slog.Default()),
		slog.Default(),
	)
	s.loops.spawn(tierRun, "checkpoint-server", func() { ckptServer.Serve(ctx, ckptConns) })

	bus, membership := st.bus, st.membership
	s.loops.spawn(tierRun, "rpc-accept", func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn, ok := <-rpcConns:
				if !ok {
					return
				}
				go quicServer.HandleConnection(conn)
			}
		}
	})
	s.loops.spawn(tierRun, "agent-event-accept", func() {
		for {
			select {
			case <-ctx.Done():
				return
			case conn, ok := <-gapiConns:
				if !ok {
					return
				}
				go handleQUICConn(conn, bus, membership)
			}
		}
	})
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "rpc and agent event planes attached to control-plane listener")
	return nil
}

// startTelemetry wires the local lifecycle subscription, the heartbeat
// publisher, the leader-side observers, and the metrics endpoint.
func (s *Supervisor) startTelemetry(ctx context.Context, st *runState) error {
	// Local lifecycle status updates the tracker: an instance whose
	// process dies publishes FAILED/STOPPED on the local bus, and the
	// next heartbeat reports it so the leader can re-place (phase 2b).
	instTracker := st.tracker
	if err := st.localBus.Subscribe("system", "", gapieventbus.TopicAgentLifecycleStatus, func(e gapieventbus.Event[*anypb.Any]) {
		var status protopkg.LifecycleStatus
		if e.Payload == nil || e.Payload.UnmarshalTo(&status) != nil {
			return
		}
		slog.Default().LogAttrs(ctx, slog.LevelDebug, "local lifecycle status",
			logattr.InstanceID(status.AgentId), slog.String("state", status.State), slog.String("message", status.Message))
		switch status.State {
		case "RUNNING":
			instTracker.SetIfTracked(status.AgentId, "running")
		case "FAILED", "STOPPED":
			// A deliberate stop removes the instance from the tracker
			// before the event arrives; anything still tracked died.
			instTracker.SetIfTracked(status.AgentId, "failed")
		}
	}); err != nil {
		return fmt.Errorf("subscribe instance status: %w", err)
	}

	s.startHeartbeat(ctx, st)
	s.subscribeClusterEvents(st)
	return s.startMetrics(ctx)
}

// startHeartbeat publishes this node's instance states cluster-wide and
// merges what it sees from peers. The heartbeat doubles as the locator
// update path (DDR-3/4/5): a running instance's pid, start epoch, and
// pid-namespace inode ride along, stamped by this node's HLC for
// last-writer-wins.
func (s *Supervisor) startHeartbeat(ctx context.Context, st *runState) {
	hlcClock := hlc.New(st.nodeID)
	nodeID, bus, tracker, sched := st.nodeID, st.bus, st.tracker, st.sched

	s.loops.spawn(tierRun, "instance-heartbeat", func() {
		ticker := time.NewTicker(scheduler.HeartbeatCadence)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for id, info := range tracker.Snapshot() {
					stamp := hlcClock.Now()
					payload := map[string]interface{}{
						"instance_id": id,
						"node_id":     nodeID,
						"state":       info.State,
						"hlc_wall":    stamp.Wall,
						"hlc_counter": stamp.Counter,
						"hlc_node":    stamp.Node,
					}
					if info.Pid > 0 {
						if pi, err := procsig.Identify(info.Pid); err == nil {
							payload["node_pid"] = pi.Pid
							payload["start_epoch"] = pi.StartEpoch
							payload["pid_ns_inode"] = pi.PidNsInode
						}
					}
					if err := bus.PublishCluster("system", "instance.heartbeat", payload, nil); err != nil {
						slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish instance heartbeat failed", logattr.InstanceID(id), logattr.Err(err))
					}
				}
			}
		}
	})

	// Leader side: heartbeats feed the reconciler's health view and the
	// locator map; the HLC merges every remote stamp it sees.
	bus.Subscribe("instance.heartbeat", func(e eventbus.Event) {
		id, _ := e.Payload["instance_id"].(string)
		node, _ := e.Payload["node_id"].(string)
		state, _ := e.Payload["state"].(string)
		if id == "" {
			return
		}
		sched.ObserveHeartbeat(id, node, state, time.Now())

		stamp := hlc.Timestamp{
			Wall:    payloadInt64(e.Payload["hlc_wall"]),
			Counter: payloadUint32(e.Payload["hlc_counter"]),
			Node:    payloadString(e.Payload["hlc_node"]),
		}
		if stamp.IsZero() {
			return
		}
		hlcClock.Observe(stamp)
		if pid := payloadInt64(e.Payload["node_pid"]); pid > 0 {
			sched.ObserveLocator(id, scheduler.Locator{
				NodeID:     node,
				Pid:        int(pid),
				StartEpoch: payloadUint64(e.Payload["start_epoch"]),
				PidNsInode: payloadUint64(e.Payload["pid_ns_inode"]),
				At:         stamp,
			})
		}
	})

	// Revocation gossip: a revoking node broadcasts its filter; every
	// node merges what it sees (the filter only grows, so merge order
	// is irrelevant). Bytes ride the bus JSON as base64.
	bus.Subscribe("capability.revocation", func(e eventbus.Event) {
		id, ok := decodeRevocationFilter(ctx, e.Payload["token_id"])
		if !ok {
			return
		}
		if len(id) != 16 {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "revocation carries a malformed token id",
				slog.Int("bytes", len(id)))
			return
		}
		s.revocations.Revoke(id)
	})
}

// subscribeClusterEvents renders notable bus traffic into the RPC
// surface's event history.
func (s *Supervisor) subscribeClusterEvents(st *runState) {
	schedulerRPC := st.schedulerRPC
	clusterEvents := []string{
		"cluster.node.joined",
		"cluster.node.left",
		"cluster.node.failed",
		"cluster.node.updated",
		"job.submitted",
		"job.assigned",
		"job.migrated",
	}

	for _, topic := range clusterEvents {
		st.bus.Subscribe(topic, func(e eventbus.Event) {
			var msg string
			switch {
			case strings.HasPrefix(e.Topic, "cluster.node."):
				action := strings.TrimPrefix(e.Topic, "cluster.node.")
				name, _ := e.Payload["name"].(string)
				msg = fmt.Sprintf("Node %s: %s", action, name)
			case strings.HasPrefix(e.Topic, "job."):
				action := strings.TrimPrefix(e.Topic, "job.")
				jobID, _ := e.Payload["job_id"].(string)
				msg = fmt.Sprintf("Job %s: %s", action, jobID)
				if nodeID, ok := e.Payload["node_id"].(string); ok {
					msg = fmt.Sprintf("%s (on %s)", msg, nodeID)
				}
			}

			if msg != "" {
				slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "cluster event", logattr.Message(msg))
				schedulerRPC.AddEvent(msg)
			}
		})
	}

	st.bus.Subscribe("global.alert", func(e eventbus.Event) {
		msg := fmt.Sprintf("Alert: %v", e.Payload)
		slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "alert event", logattr.Message(msg))
		schedulerRPC.AddEvent(msg)
	})
}

// startMetrics serves the Prometheus endpoint on its own port - the
// only listener outside the shared control plane.
//
// The server gets an explicit Shutdown on cancellation. Without it
// ListenAndServe never observes the context at all, and joining this
// loop would block every shutdown for the full grace period
// (GOBLIN-DIV-038).
//
// The handler goes on a per-server mux rather than http.DefaultServeMux:
// the global mux made a second Run in one process panic on duplicate
// registration, which is hidden global state the Go manifesto forbids
// without justification.
func (s *Supervisor) startMetrics(ctx context.Context) error {
	if s.cfg.MetricsAddr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	srv := &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.loops.spawn(tierRun, "metrics-server", func() {
		slog.Default().LogAttrs(ctx, slog.LevelInfo, "metrics server listening", logattr.Addr(s.cfg.MetricsAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "metrics server failed", logattr.Err(err))
		}
	})
	s.loops.spawn(tierRun, "metrics-closer", func() {
		<-ctx.Done()
		// Background: the run context is already cancelled, and passing
		// it here would abort the graceful close immediately.
		if err := srv.Shutdown(context.Background()); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "metrics server shutdown failed", logattr.Err(err))
		}
	})
	return nil
}

// startMonitors runs the reconciler and the cluster stats loop.
func (s *Supervisor) startMonitors(ctx context.Context, st *runState) {
	// The reconcile loop itself: leader-gated inside, kickable via
	// RegisterGlobalAgent/ScaleAgent for sub-interval placement latency.
	sched := st.sched
	s.loops.spawn(tierRun, "reconciler", func() { sched.RunReconciler(ctx, 2*time.Second) })

	engine, membership, schedulerRPC := st.consensus, st.membership, st.schedulerRPC
	s.loops.spawn(tierRun, "cluster-monitor", func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		var lastRaftState string
		var lastLeaderID string

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stats := engine.Stats()
				if term, ok := stats["term"]; ok {
					val, _ := strconv.ParseFloat(term, 64)
					metrics.RaftTerm.Set(val)
				}

				state := stats["state"]
				if lastRaftState != "" && state != lastRaftState {
					schedulerRPC.AddEvent(fmt.Sprintf("Raft State Change: %s -> %s", lastRaftState, state))
				}
				lastRaftState = state

				leaderID := engine.LeaderID()
				if leaderID != lastLeaderID {
					msg := "Leader Election: Lost leader"
					if leaderID != "" {
						msg = fmt.Sprintf("Leader Election: New Leader is %s", leaderID)
					}
					slog.Default().LogAttrs(ctx, slog.LevelInfo, "cluster event", logattr.Message(msg))
					schedulerRPC.AddEvent(msg)
				}
				lastLeaderID = leaderID

				var stateVal float64
				switch state {
				case "Leader":
					stateVal = 2
				case "Candidate":
					stateVal = 1
				default:
					stateVal = 0
				}
				metrics.RaftState.Set(stateVal)

				alive, failed, left := 0, 0, 0
				for _, m := range membership.Members() {
					switch m.Status {
					case serf.StatusAlive:
						alive++
					case serf.StatusFailed:
						failed++
					case serf.StatusLeft:
						left++
					}
				}
				metrics.ClusterMembers.WithLabelValues("alive").Set(float64(alive))
				metrics.ClusterMembers.WithLabelValues("failed").Set(float64(failed))
				metrics.ClusterMembers.WithLabelValues("left").Set(float64(left))
			}
		}
	})

	slog.Default().LogAttrs(ctx, slog.LevelInfo, "listening for cluster events")
}
