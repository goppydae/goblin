package supervisor

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/goppydae/gapi/core/clock"
	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/gapi/core/metrics"
	"github.com/goppydae/gapi/core/store"
	"github.com/goppydae/gapi/core/version"
	"github.com/goppydae/gapi/internal/agentmgr"
	"github.com/goppydae/gapi/internal/agentreg"
	"github.com/goppydae/gapi/internal/cgroups"
	"github.com/goppydae/gapi/internal/eventbus"
	"github.com/goppydae/gapi/internal/lifecycle"
	"github.com/goppydae/gapi/internal/logging/logcore"
	"github.com/goppydae/gapi/internal/logging/logevent"
	protopkg "github.com/goppydae/gapi/internal/proto"
	"github.com/goppydae/gapi/internal/transport"
	"github.com/rs/zerolog"
)

// Supervisor manages the GAPI runtime lifecycle.
type Supervisor struct {
	cfg           *config.Config
	logger        zerolog.Logger
	manager       *agentmgr.AgentManager
	bus           *eventbus.EventBus[*anypb.Any]
	registry      *agentreg.AgentRegistry
	host          string
	metricsServer *metrics.Server
	clock         clock.Clock
}

// New creates a new Supervisor instance.
func New(cfg *config.Config) (*Supervisor, error) {
	logger := logcore.With().Str("module", "gapid").Logger()
	host, _ := os.Hostname()

	// Cgroups setup
	if err := cgroups.Setup(); err != nil {
		logger.Warn().Err(err).Msg("failed to setup cgroups, resource limits will be unavailable")
	} else {
		logger.Info().Msg("cgroups setup complete")
	}

	// Transport
	t, err := transport.NewServerFromConfig(cfg.Transport)
	if err != nil {
		return nil, fmt.Errorf("transport init: %w", err)
	}

	bus := eventbus.NewEventBus[*anypb.Any](t)
	typedBus := lifecycle.TypedBus{}

	// Agent Manager
	pyRunner := resolvePyRunner()
	manager := agentmgr.NewAgentManager(bus, &typedBus, pyRunner)

	// Store & Registry
	raw, err := store.Open(store.Hybrid)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to open database")
	}

	// Security: Verification Key
	var pubKey *ed25519.PublicKey
	// Check config first, then env
	kp := cfg.Security.VerifyKey
	if kp == "" {
		kp = os.Getenv("GAPI_VERIFY_KEY")
	}

	if kp != "" {
		pk, err := crypto.LoadPublic(kp)
		if err != nil {
			return nil, fmt.Errorf("failed to load verification key %q: %w", kp, err)
		}
		logger.Debug().Str("key_path", kp).Msg("integrity verification enabled")
		pubKey = &pk
	}

	db, ok := raw.(store.HybridStore)
	if !ok {
		// Should we fail hard? Logic in cmd did not return error, but printed specific error inside NewAgentRegistry if cast failed?
		// Actually cmd checked cast.
		return nil, fmt.Errorf("failed to cast store to HybridStore")
	}
	registry, err := agentreg.NewAgentRegistry(db, pubKey)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create agent registry")
	}

	s := &Supervisor{
		cfg:      cfg,
		logger:   logger,
		manager:  manager,
		bus:      bus,
		registry: registry,
		host:     host,
		clock:    clock.RealClock{},
	}

	// Initialize build info metrics and create server if enabled
	if cfg.Metrics.Enabled {
		metrics.BuildInfo.WithLabelValues(
			version.GAPIVersion,
			version.Commit,
			runtime.Version(),
		).Set(1)

		s.metricsServer = metrics.NewServer(cfg.Metrics.Addr, logger)
		logger.Debug().Str("addr", cfg.Metrics.Addr).Msg("metrics enabled")
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
	s.logger.Info().Msg("starting gapid supervisor")

	// Setup Agents
	s.setupAgents()

	// Register Event Handlers
	s.registerHandlers()

	s.logger.Info().Str("host", s.host).Msg("supervisor running")

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
		s.logger.Info().Msg("metrics collection started")

		// Start metrics HTTP server
		if s.metricsServer != nil {
			go func() {
				if err := s.metricsServer.Start(); err != nil {
					s.logger.Error().Err(err).Msg("metrics server failed")
				}
			}()
			s.logger.Info().Str("addr", s.cfg.Metrics.Addr).Msg("metrics server started")
		}
	}

	// Wait for context done
	<-ctx.Done()

	s.logger.Warn().Msg("received shutdown signal via context")

	// Shutdown metrics server if running
	if s.metricsServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.metricsServer.Stop(shutdownCtx); err != nil {
			s.logger.Error().Err(err).Msg("metrics server shutdown failed")
		} else {
			s.logger.Info().Msg("metrics server stopped")
		}
	}

	if err := s.manager.StopAll(); err != nil {
		s.logger.Error().Err(err).Msg("graceful stop of all agents failed")
	}

	logevent.Lifecycle(s.logger, "gapid", "stop", "gapid", version.BinaryVersion())
	s.logger.Info().Msg("exited cleanly")
	return nil
}

func (s *Supervisor) setupAgents() {
	s.logger.Info().Msg("performing agent discovery and setup")

	// Use new search path system
	discovered, err := s.manager.DiscoverFromPaths()
	if err != nil {
		s.logger.Error().Err(err).Msg("Agent discovery failed")
		return
	}

	// Register with DB
	var foundIDs []string
	for _, desc := range discovered {
		id := desc["id"]
		foundIDs = append(foundIDs, id)
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
			s.logger.Error().Err(err).Str("agent_id", ad.ID).Msg("failed to register discovered agent")
		}
	}

	// Topological startup
	sortedIDs, err := s.manager.TopologicalSort()
	if err != nil {
		s.logger.Warn().Err(err).Msg("topological sort failed, falling back to random order")
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
		s.logger.Warn().Msg("no agents registered in manager")
	} else {
		for _, id := range sortedIDs {
			// Integrity check
			if _, err := s.registry.Lookup(id); err != nil {
				s.logger.Warn().Str("agent_id", id).Msg("skipping startup of unregistered agent (integrity failure?)")
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
				s.logger.Warn().Str("agent_id", id).Str("missing_dependency", missingReq).Msg("skipping start due to missing or failed dependency")
				continue
			}

			s.logger.Debug().Str("agent_id", id).Msg("registered agent")

			desc := ag.Describe()
			started := false

			// lazy Activation
			if desc["listen_stream"] != "" {
				if armable, ok := ag.(interface {
					Arm() error
					SetTrafficHandler(func())
					Controller() *lifecycle.Controller
				}); ok {
					ctrl := armable.Controller()
					armable.SetTrafficHandler(func() {
						s.logger.Info().Str("agent_id", id).Msg("traffic detected, triggering lazy start")
						if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
							s.logger.Error().Err(err).Str("agent_id", id).Msg("lazy start failed")
						}
					})
					if err := armable.Arm(); err != nil {
						s.logger.Error().Err(err).Str("agent_id", id).Msg("failed to arm lazy activation")
					} else {
						s.logger.Info().Str("agent_id", id).Msg("armed lazy activation")
						started = true
					}
				}
			}

			// Timer auto-start
			if desc["type"] == "timer" {
				ctrl := ag.Controller()
				if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
					s.logger.Error().Err(err).Str("agent_id", id).Msg("failed to start timer agent")
				} else {
					s.logger.Info().Str("agent_id", id).Msg("timer agent started")
					started = true
				}
			}

			// Standard Service/Oneshot auto-start (if not lazy/timer)
			// (We assume 'service' or 'oneshot' type and no listen_stream means it should start immediately)
			if (desc["type"] == "service" || desc["type"] == "oneshot") && desc["listen_stream"] == "" {
				ctrl := ag.Controller()
				// We should verify enabled? implicit enabled for now.
				if err := ctrl.Apply(lifecycle.ActionStart); err != nil {
					s.logger.Error().Err(err).Str("agent_id", id).Msg("failed to start agent")
				} else {
					s.logger.Info().Str("agent_id", id).Msg("agent started")
					started = true
				}
			}

			if started {
				startedAgents[id] = true
			}
		}
	}

	s.logger.Info().Msg("agent setup complete")
}

func (s *Supervisor) registerHandlers() {
	// Ping/Pong
	err := s.bus.SubscribePrefix("system", "", "ping", func(e eventbus.Event[*anypb.Any]) {
		s.logger.Debug().Str("event", "handling_ping").Str("event_id", e.ID).Msg("received ping, preparing pong")
		logevent.Lifecycle(s.logger, "gapid", "handle_ping", "gapid", version.BinaryVersion())

		pong := &protopkg.PingStatus{Status: "pong"}
		anyPayload, err := anypb.New(pong)
		if err != nil {
			s.logger.Error().Err(err).Msg("failed to pack pong payload")
			return
		}

		response := eventbus.NewEvent("system", "", "pong", "gapid", anyPayload, true)
		_ = s.bus.Publish(response)
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to subscribe to ping event")
	}

	// Agent Status
	err = s.bus.SubscribePrefix("system", "", "agents/", func(e eventbus.Event[*anypb.Any]) {
		s.logger.Debug().Str("event_id", e.ID).Msg("received agent status request")

		entries, err := s.registry.List()
		if err != nil {
			s.logger.Error().Err(err).Msg("failed to list agents")
			return
		}

		var agentStatuses []*protopkg.AgentStatus
		for _, entry := range entries {
			st := protopkg.AgentState_AGENT_STATE_UNKNOWN
			if ag := getAgentCI(s.manager, entry.ID); ag != nil {
				st = mapStateToProto(ag.Controller().State())
			}
			deps, err := s.registry.GetDependencies(entry.ID)
			if err != nil {
				deps = entry.Requires
				s.logger.Warn().Err(err).Str("agent", entry.ID).Msg("failed to resolve graph deps")
			}

			// Collect metrics from cgroups if available
			var cpuUsage float64
			var memUsage uint64
			cgName := fmt.Sprintf("gapid-%s", entry.ID)
			if stats, err := cgroups.GetStats(cgName); err == nil {
				cpuUsage = stats.CPUUsage
				memUsage = uint64(stats.MemoryUsage)
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
			s.logger.Error().Err(err).Msg("failed to pack agent status response")
			return
		}

		response := eventbus.NewEvent("system", "", "agents.reply", "gapid", anyPayload, true)
		_ = s.bus.Publish(response)
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to subscribe to agents event")
	}

	// Reload
	err = s.bus.Subscribe("system", "", "agent.reload", func(e eventbus.Event[*anypb.Any]) {
		s.logger.Debug().Str("event_id", e.ID).Msg("received agent reload request")
		s.setupAgents()
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to subscribe to reload event")
	}

	// Lifecycle Actions
	err = s.bus.Subscribe("system", "", "agent/lifecycle.action", func(e eventbus.Event[*anypb.Any]) {
		s.handleLifecycleAction(e)
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to subscribe to lifecycle event")
	}
}

func (s *Supervisor) handleLifecycleAction(e eventbus.Event[*anypb.Any]) {
	s.logger.Debug().Str("event_id", e.ID).Str("topic", e.Topic).Msg("received lifecycle control event")

	var cmd protopkg.LifecycleControl
	if err := eventbus.UnmarshalAnyPayload(e, &cmd); err != nil {
		s.logger.Error().Err(err).Msg("failed to decode LifecycleControl")
		s.replyStatus(cmd.GetAgentId(), "FAILED", "decode error: "+err.Error())
		return
	}

	targetID := strings.TrimSpace(cmd.GetAgentId())
	ag := getAgentCI(s.manager, targetID)
	if ag == nil {
		s.logger.Warn().Str("agent_id", targetID).Msg("unknown agent in lifecycle control")
		s.replyStatus(targetID, "FAILED", "unknown agent")
		return
	}

	// ACK
	s.replyStatus(targetID, "PENDING", "accepted")

	action := actionFromEnum(cmd.GetAction())
	desc := ag.Describe()
	var applyErr error
	switch action {
	case "initialize":
		applyErr = ag.Controller().Apply(lifecycle.ActionInitialize)
	case "start":
		applyErr = ag.Controller().Apply(lifecycle.ActionStart)
		if applyErr == nil {
			// Record successful start
			metrics.RecordAgentStart(targetID, desc["type"])
		}
	case "stop":
		applyErr = ag.Controller().Apply(lifecycle.ActionStop)
		if applyErr == nil {
			// Record successful stop
			metrics.RecordAgentStop(targetID, desc["type"])
		}
	case "reload":
		applyErr = ag.Controller().Apply(lifecycle.ActionReload)
	case "restart":
		applyErr = ag.Controller().Apply(lifecycle.ActionRestart)
		if applyErr == nil {
			// Record restart as stop + start
			metrics.RecordAgentStop(targetID, desc["type"])
			metrics.RecordAgentStart(targetID, desc["type"])
		}
	default:
		applyErr = fmt.Errorf("unknown action %q", action)
	}

	if applyErr != nil {
		finalState := ag.Controller().State()
		wanted := action
		isOkStart := (wanted == "start") && (strings.EqualFold(finalState, lifecycle.StateRunning) || strings.EqualFold(finalState, lifecycle.StateStarting))
		isOkStop := (wanted == "stop") && (strings.EqualFold(finalState, lifecycle.StateStopped) || strings.EqualFold(finalState, lifecycle.StateStopping))

		state := finalState
		msg := "ok"
		if !(isOkStart || isOkStop) {
			state = lifecycle.StateError
			msg = applyErr.Error()
			// Record failure
			metrics.RecordAgentFailure(targetID, desc["type"], action)
		}

		s.replyStatus(targetID, state, msg)
		s.logger.Error().Err(applyErr).Str("agent_id", targetID).Str("action", action).Str("state", finalState).Msg("lifecycle apply returned error; replied with observed state")
		return
	}

	// Wait for settlement
	deadline := s.clock.Now().Add(20 * time.Second)
	finalState := ag.Controller().State()
	for isInFlight(finalState) && s.clock.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		finalState = ag.Controller().State()
	}

	msg := "ok"
	if isInFlight(finalState) {
		msg = "finalize timeout; current state=" + finalState
	}

	s.replyStatus(targetID, finalState, msg)
	s.logger.Info().Str("agent_id", targetID).Str("action", action).Str("state", finalState).Msg("lifecycle applied (final)")
}

func (s *Supervisor) replyStatus(agentID, state, msg string) {
	if anyPayload, err := anypb.New(&protopkg.LifecycleStatus{
		AgentId:  agentID,
		State:    state,
		Message:  msg,
		Time:     timestamppb.Now(),
		Hostname: s.host,
	}); err == nil {
		resp := eventbus.NewEvent("system", "", "agent/lifecycle.status", "gapid", anyPayload, true)
		_ = s.bus.Publish(resp)
	}
}

// Helpers duplicated from original but now unexported helpers of Supervisor or package
func mapStateToProto(s string) protopkg.AgentState {
	switch s {
	case lifecycle.StateInitializing:
		return protopkg.AgentState_AGENT_STATE_INITIALIZED
	case lifecycle.StateStarting:
		return protopkg.AgentState_AGENT_STATE_STARTING
	case lifecycle.StateRunning:
		return protopkg.AgentState_AGENT_STATE_RUNNING
	case lifecycle.StateStopping:
		return protopkg.AgentState_AGENT_STATE_STOPPING
	case lifecycle.StateStopped:
		return protopkg.AgentState_AGENT_STATE_STOPPED
	case lifecycle.StateReloading:
		return protopkg.AgentState_AGENT_STATE_RELOADING
	case lifecycle.StateRestarting:
		return protopkg.AgentState_AGENT_STATE_STARTING
	case lifecycle.StateError:
		return protopkg.AgentState_AGENT_STATE_FAILED
	default:
		return protopkg.AgentState_AGENT_STATE_UNKNOWN
	}
}

func getAgentCI(mgr *agentmgr.AgentManager, id string) interface {
	Controller() *lifecycle.Controller
	Describe() map[string]string
} {
	if id == "" {
		return nil
	}
	if ag := mgr.Get(id); ag != nil {
		return ag
	}
	idLower := strings.ToLower(id)
	allAgents := mgr.All()
	ids := make([]string, 0, len(allAgents))
	for k := range allAgents {
		ids = append(ids, k)
	}
	sort.Strings(ids)

	for _, k := range ids {
		ag := allAgents[k]
		if strings.ToLower(k) == idLower {
			return ag
		}
		if desc := ag.Describe(); strings.ToLower(desc["id"]) == idLower {
			return ag
		}
	}
	return nil
}

func isInFlight(state string) bool {
	s := strings.ToUpper(strings.TrimSpace(state))
	switch s {
	case "PENDING", "STARTING", "STOPPING", "RELOADING", "INITIALIZING", "":
		return true
	default:
		return false
	}
}

func resolvePyRunner() string {
	if v := os.Getenv("GAPI_PY_RUNNER"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cand := filepath.Join(dir, "adk", "python", "agent", "runner.py")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return filepath.Join("adk", "python", "agent", "runner.py")
}

func resolveAgentsDir(cfg *config.Config) string {
	if v := os.Getenv("GAPI_AGENTS_DIR"); v != "" {
		return v
	}
	type agentsDirGetter interface{ AgentsDir() string }
	if g, ok := any(cfg).(agentsDirGetter); ok {
		if dir := g.AgentsDir(); dir != "" {
			return dir
		}
	}
	if _, err := os.Stat("agents"); err == nil {
		return "agents"
	}
	return filepath.Join("gapi", "agents")
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func actionFromEnum(act protopkg.LifecycleControl_Action) string {
	switch act {
	case protopkg.LifecycleControl_START:
		return "start"
	case protopkg.LifecycleControl_STOP:
		return "stop"
	case protopkg.LifecycleControl_RELOAD:
		return "reload"
	case protopkg.LifecycleControl_RESTART:
		return "restart"
	default:
		return "initialize"
	}
}
