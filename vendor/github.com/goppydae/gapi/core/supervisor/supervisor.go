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
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/gapi/core/cgroups"
	"github.com/goppydae/gapi/core/clock"
	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/core/metrics"
	"github.com/goppydae/gapi/core/product"
	shutdownpkg "github.com/goppydae/gapi/core/shutdown"
	"github.com/goppydae/gapi/core/store"
	"github.com/goppydae/gapi/core/transport"
	"github.com/goppydae/gapi/core/version"
	"github.com/goppydae/gapi/internal/agentreg"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// Supervisor manages the GAPI runtime lifecycle.
type Supervisor struct {
	cfg           *config.Config
	logger        *slog.Logger
	manager       *agentmgr.AgentManager
	bus           *eventbus.EventBus[*anypb.Any]
	registry      *agentreg.AgentRegistry
	host          string
	metricsServer *metrics.Server
	clock         clock.Clock
	// shutdownReq carries system shutdown requests (PID-1 signals and
	// the system.shutdown bus topic); buffered so the first request
	// wins and repeats are absorbed.
	shutdownReq chan shutdownpkg.Action
}

// New creates a new Supervisor instance.
func New(cfg *config.Config) (*Supervisor, error) {
	logger := slog.Default().With(logattr.Module("supervisor"))
	host, _ := os.Hostname()

	// Cgroups setup
	if err := cgroups.Setup(); err != nil {
		logger.LogAttrs(context.Background(), slog.LevelWarn, "failed to setup cgroups, resource limits will be unavailable", logattr.Err(err))
	} else {
		logger.LogAttrs(context.Background(), slog.LevelInfo, "cgroups setup complete")
	}

	// Transport
	t, err := transport.NewServerFromConfig(cfg.Transport)
	if err != nil {
		return nil, fmt.Errorf("transport init: %w", err)
	}
	publishControlAddr(logger, t)

	bus := eventbus.NewEventBus[*anypb.Any](t)
	typedBus := lifecycle.TypedBus{}

	// Security: Verification Key (loaded before the agent manager so
	// production-mode discovery can verify signatures; review R20)
	var pubKey *ed25519.PublicKey
	// Check config first, then env
	kp := cfg.Security.VerifyKey
	if kp == "" {
		kp = os.Getenv(product.EnvKey("VERIFY_KEY"))
	}

	if kp != "" {
		pk, err := crypto.LoadPublic(kp)
		if err != nil {
			return nil, fmt.Errorf("failed to load verification key %q: %w", kp, err)
		}
		logger.LogAttrs(context.Background(), slog.LevelDebug, "integrity verification enabled", logattr.KeyPath(kp))
		pubKey = &pk
	}

	pyRunner := resolvePyRunner(logger)
	var discoveryKey ed25519.PublicKey
	if pubKey != nil {
		discoveryKey = *pubKey
	}
	manager := agentmgr.NewAgentManager(bus, &typedBus, pyRunner, cfg.Supervisor.ProductionMode, discoveryKey)

	// Store & Registry
	raw, err := store.Open(store.Hybrid)
	if err != nil {
		// The registry is not optional: without it, agent integrity verification
		// is silently skipped and later lookups panic on a nil registry. Fail
		// construction instead of returning a half-built supervisor.
		return nil, fmt.Errorf("open store: %w", err)
	}

	db, ok := raw.(store.HybridStore)
	if !ok {
		// Should we fail hard? Logic in cmd did not return error, but printed specific error inside NewAgentRegistry if cast failed?
		// Actually cmd checked cast.
		return nil, fmt.Errorf("failed to cast store to HybridStore")
	}
	registry, err := agentreg.NewAgentRegistry(db, pubKey)
	if err != nil {
		return nil, fmt.Errorf("create agent registry: %w", err)
	}

	s := &Supervisor{
		cfg:         cfg,
		logger:      logger,
		manager:     manager,
		bus:         bus,
		registry:    registry,
		host:        host,
		clock:       clock.RealClock{},
		shutdownReq: make(chan shutdownpkg.Action, 1),
	}

	// Initialize build info metrics and create server if enabled
	if cfg.Metrics.Enabled {
		metrics.BuildInfo.WithLabelValues(
			version.GAPIVersion,
			version.Commit,
			runtime.Version(),
		).Set(1)

		s.metricsServer = metrics.NewServer(cfg.Metrics.Addr, logger)
		logger.LogAttrs(context.Background(), slog.LevelDebug, "metrics enabled", logattr.Addr(cfg.Metrics.Addr))
	}

	return s, nil
}

// Bus returns the internal event bus.
func (s *Supervisor) Bus() *eventbus.EventBus[*anypb.Any] {
	return s.bus
}

// Start runs the supervisor logic. It blocks until Stop is called or an error occurs.

// Note: In a real library, Start might be non-blocking or accept a context.
// For now, we mirror the existing blocking behavior but allow external control via context cancellation if needed?
// The original used `runSupervisor()` which blocked on signal.
// We'll expose `Run()` which sets up handlers and blocks.
func (s *Supervisor) Run(ctx context.Context) error {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "starting supervisor")

	// Setup Agents
	s.setupAgents()

	// Register Event Handlers
	s.registerHandlers()

	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "supervisor running", logattr.Host(s.host))

	// Start periodic metrics collection if enabled
	var metricsTicker *time.Ticker
	if s.cfg.Metrics.Enabled {
		metricsTicker = time.NewTicker(15 * time.Second)
		defer metricsTicker.Stop()

		startTime := s.clock.Now()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-metricsTicker.C:
					s.collectMetrics(startTime)
				}
			}
		}()
		s.logger.LogAttrs(context.Background(), slog.LevelInfo, "metrics collection started")

		// Start metrics HTTP server
		if s.metricsServer != nil {
			go func() {
				if err := s.metricsServer.Start(); err != nil {
					s.logger.LogAttrs(ctx, slog.LevelError, "metrics server failed", logattr.Err(err))
				}
			}()
			s.logger.LogAttrs(context.Background(), slog.LevelInfo, "metrics server started", logattr.Addr(s.cfg.Metrics.Addr))
		}
	}

	// Wait for context done
	<-ctx.Done()

	unpublishControlAddr(s.logger)

	s.logger.LogAttrs(context.Background(), slog.LevelWarn, "received shutdown signal via context")

	// Shutdown metrics server if running
	if s.metricsServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), config.SupervisorShutdownTimeout)
		defer cancel()
		if err := s.metricsServer.Stop(shutdownCtx); err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "metrics server shutdown failed", logattr.Err(err))
		} else {
			s.logger.LogAttrs(context.Background(), slog.LevelInfo, "metrics server stopped")
		}
	}

	if err := s.manager.StopAll(); err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "graceful stop of all agents failed", logattr.Err(err))
	}

	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "lifecycle event",
		logattr.Event("lifecycle"), logattr.Source("supervisor"), logattr.Action("stop"),
		logattr.AgentID("supervisor"), logattr.Version(version.BinaryVersion()))
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "exited cleanly")
	return nil
}

func (s *Supervisor) setupAgents() {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "performing agent discovery and setup")

	// Use new search path system
	discovered, err := s.manager.DiscoverFromPaths()
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "Agent discovery failed", logattr.Err(err))
		return
	}

	// Register with DB
	for _, desc := range discovered {
		id := desc["id"]
		ad := &agentreg.AgentDescription{
			ID:           id,
			Path:         desc["path"],
			Type:         desc["type"],
			Language:     desc["language"],
			Version:      desc["version"],
			Hash:         desc["hash"],
			Tags:         splitCSV(desc["tags"]),
			Requires:     splitCSV(desc["requires"]),
			Wants:        splitCSV(desc["wants"]),
			WantedBy:     splitCSV(desc["wanted_by"]),
			RequiredBy:   splitCSV(desc["required_by"]),
			Capabilities: splitCSV(desc["capabilities"]),
		}
		if len(ad.Requires) == 0 && desc["deps"] != "" {
			ad.Requires = splitCSV(desc["deps"])
		}
		if err := s.registry.Register(ad); err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to register discovered agent", logattr.Err(err), logattr.AgentID(ad.ID))
		}
	}

	// Topological startup
	sortedIDs, err := s.manager.TopologicalSort()
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "topological sort failed, falling back to random order", logattr.Err(err))
		allAgents := s.manager.All()
		ids := make([]string, 0, len(allAgents))
		for id := range allAgents {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		sortedIDs = append(sortedIDs, ids...)
	}

	// Track successfully started/armed agents for dependency resolution
	startedAgents := make(map[string]bool)

	if len(sortedIDs) == 0 {
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "no agents registered in manager")
	} else {
		for _, id := range sortedIDs {
			// Integrity check
			if _, err := s.registry.Lookup(id); err != nil {
				s.logger.LogAttrs(context.Background(), slog.LevelWarn, "skipping startup of unregistered agent (integrity failure?)", logattr.AgentID(id))
				continue
			}

			ag := s.manager.Get(id)
			if ag == nil {
				continue
			}

			// Dependency Check
			// We only enforce 'Requires'. 'Wants' are advisory.
			missingReq := ""
			for _, req := range ag.Requires() {
				if !startedAgents[req] {
					missingReq = req
					break
				}
			}
			if missingReq != "" {
				s.logger.LogAttrs(context.Background(), slog.LevelWarn, "skipping start due to missing or failed dependency", logattr.AgentID(id), logattr.MissingDependency(missingReq))
				continue
			}

			s.logger.LogAttrs(context.Background(), slog.LevelDebug, "registered agent", logattr.AgentID(id))

			desc := ag.Describe()
			started := false

			// A disabled agent is registered but never auto-started -
			// the systemd model. It stays visible to 'gapictl agent
			// status' and can still be started explicitly through the
			// lifecycle verbs; only the automatic paths below are
			// skipped. Anything not carrying the flag counts as enabled
			// (agentmgr.AgentEnabled), so a runner that predates this
			// cannot become silently un-startable.
			if !agentmgr.AgentEnabled(ag) {
				s.logger.LogAttrs(context.Background(), slog.LevelInfo,
					"agent registered but not auto-started (ENABLED is false)",
					logattr.AgentID(id))
				continue
			}

			// lazy Activation
			if desc["listen_stream"] != "" {
				if armable, ok := ag.(interface {
					Arm() error
					SetTrafficHandler(func())
					Controller() *lifecycle.Controller
				}); ok {
					ctrl := armable.Controller()
					armable.SetTrafficHandler(func() {
						s.logger.LogAttrs(context.Background(), slog.LevelInfo, "traffic detected, triggering lazy start", logattr.AgentID(id))
						if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
							s.logger.LogAttrs(context.Background(), slog.LevelError, "lazy start failed", logattr.Err(err), logattr.AgentID(id))
						}
					})
					if err := armable.Arm(); err != nil {
						s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to arm lazy activation", logattr.Err(err), logattr.AgentID(id))
					} else {
						s.logger.LogAttrs(context.Background(), slog.LevelInfo, "armed lazy activation", logattr.AgentID(id))
						started = true
					}
				}
			}

			// Timer auto-start
			if desc["type"] == "timer" {
				ctrl := ag.Controller()
				if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
					s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to start timer agent", logattr.Err(err), logattr.AgentID(id))
				} else {
					s.logger.LogAttrs(context.Background(), slog.LevelInfo, "timer agent started", logattr.AgentID(id))
					started = true
				}
			}

			// Standard Service/Oneshot auto-start (if not lazy/timer)
			// (We assume 'service' or 'oneshot' type and no listen_stream means it should start immediately)
			if (desc["type"] == "service" || desc["type"] == "oneshot") && desc["listen_stream"] == "" {
				ctrl := ag.Controller()
				if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
					s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to start agent", logattr.Err(err), logattr.AgentID(id))
				} else {
					s.logger.LogAttrs(context.Background(), slog.LevelInfo, "agent started", logattr.AgentID(id))
					started = true
				}
			}

			if started {
				startedAgents[id] = true
			}
		}
	}

	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "agent setup complete")
}

func (s *Supervisor) registerHandlers() {
	s.subscribeSystemShutdown()
	// Ping/Pong
	err := s.bus.SubscribePrefix("system", "", "ping", func(e eventbus.Event[*anypb.Any]) {
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "received ping, preparing pong", logattr.Event("handling_ping"), logattr.EventID(e.ID))
		s.logger.LogAttrs(context.Background(), slog.LevelInfo, "lifecycle event",
			logattr.Event("lifecycle"), logattr.Source("supervisor"), logattr.Action("handle_ping"),
			logattr.AgentID("supervisor"), logattr.Version(version.BinaryVersion()))

		pong := &protopkg.PingStatus{Status: "pong"}
		anyPayload, err := anypb.New(pong)
		if err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to pack pong payload", logattr.Err(err))
			return
		}

		response := eventbus.NewEvent("system", "", "pong", "gapid", anyPayload, true)
		response.ID = e.ID // correlate reply to the originating request
		_ = s.bus.Publish(response)
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to ping event", logattr.Err(err))
	}

	// Agent Status
	err = s.bus.SubscribePrefix("system", "", "agents/", func(e eventbus.Event[*anypb.Any]) {
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "received agent status request", logattr.EventID(e.ID))

		entries, err := s.registry.List()
		if err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to list agents", logattr.Err(err))
			return
		}

		var agentStatuses []*protopkg.AgentStatus
		for _, entry := range entries {
			st := protopkg.AgentState_AGENT_STATE_UNSPECIFIED
			if ag := getAgentCI(s.manager, entry.ID); ag != nil {
				st = mapStateToProto(ag.Controller().State())
			}
			deps, err := s.registry.GetDependencies(entry.ID)
			if err != nil {
				deps = entry.Requires
				s.logger.LogAttrs(context.Background(), slog.LevelWarn, "failed to resolve graph deps", logattr.Err(err), logattr.AgentID(entry.ID))
			}

			// Collect metrics from cgroups if available
			var cpuUsage float64
			var memUsage uint64
			cgName := cgroups.AgentCgroup(entry.ID)
			if stats, err := cgroups.GetStats(cgName); err == nil {
				cpuUsage = stats.CPUUsage
				if stats.MemoryUsage > 0 {
					memUsage = uint64(stats.MemoryUsage)
				}
			}

			// Calculate uptime
			var uptimeNs int64 = 0
			if uptimeable, ok := getAgentCI(s.manager, entry.ID).(interface{ Uptime() time.Duration }); ok {
				uptimeNs = int64(uptimeable.Uptime())
			}

			agentStatuses = append(agentStatuses, &protopkg.AgentStatus{
				Id:           entry.ID,
				Type:         entry.Type,
				State:        st,
				Dependencies: deps,
				Capabilities: entry.Capabilities,
				CpuUsage:     cpuUsage,
				MemoryUsage:  memUsage,
				UptimeNs:     uptimeNs,
			})
		}

		reply := &protopkg.AgentStatusResponse{Agents: agentStatuses}
		anyPayload, err := anypb.New(reply)
		if err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to pack agent status response", logattr.Err(err))
			return
		}

		response := eventbus.NewEvent("system", "", "agents.reply", "gapid", anyPayload, true)
		response.ID = e.ID // correlate reply to the originating request
		_ = s.bus.Publish(response)
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to agents event", logattr.Err(err))
	}

	// Reload
	err = s.bus.Subscribe("system", "", "agent.reload", func(e eventbus.Event[*anypb.Any]) {
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "received agent reload request", logattr.EventID(e.ID))
		s.setupAgents()
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to reload event", logattr.Err(err))
	}

	// Lifecycle Actions
	err = s.bus.Subscribe("system", "", eventbus.TopicAgentLifecycleAction, func(e eventbus.Event[*anypb.Any]) {
		s.handleLifecycleAction(e)
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to lifecycle event", logattr.Err(err))
	}
}
