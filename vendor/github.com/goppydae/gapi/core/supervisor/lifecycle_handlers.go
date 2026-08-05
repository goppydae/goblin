// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Lifecycle-action handling for the runtime supervisor: decoding
// LifecycleControl commands off the bus, driving the agent manager,
// and answering with LifecycleStatus. Split from supervisor.go to keep
// both files inside the source-size limit.

package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goppydae/gapi/core/agentmgr"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/lifecycle"
	"github.com/goppydae/gapi/core/metrics"
	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *Supervisor) handleLifecycleAction(e eventbus.Event[*anypb.Any]) {
	s.logger.LogAttrs(context.Background(), slog.LevelDebug, "received lifecycle control event", logattr.EventID(e.ID), logattr.Topic(e.Topic))

	var cmd protopkg.LifecycleControl
	if err := eventbus.UnmarshalAnyPayload(e, &cmd); err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to decode LifecycleControl", logattr.Err(err))
		s.replyStatus(cmd.GetAgentId(), "FAILED", "decode error: "+err.Error())
		return
	}

	targetID := strings.TrimSpace(cmd.GetAgentId())
	ag := getAgentCI(s.manager, targetID)
	if ag == nil {
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "unknown agent in lifecycle control", logattr.AgentID(targetID))
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
		if !isOkStart && !isOkStop {
			state = lifecycle.StateError
			msg = applyErr.Error()
			// Record failure
			metrics.RecordAgentFailure(targetID, desc["type"], action)
		}

		s.replyStatus(targetID, state, msg)
		s.logger.LogAttrs(context.Background(), slog.LevelError, "lifecycle apply returned error; replied with observed state", logattr.Err(applyErr), logattr.AgentID(targetID), logattr.Action(action), slog.String("state", finalState))
		return
	}

	// Controller.Apply is synchronous: it only returns once the agent has
	// reached a terminal state (it internally awaits running/stopped via the
	// event bus). So the observed state is already settled here - no busy poll
	// loop is needed. If a future async runner leaves it in flight, surface that
	// rather than silently sleeping.
	finalState := ag.Controller().State()

	msg := "ok"
	if isInFlight(finalState) {
		msg = "still settling; current state=" + finalState
	}

	s.replyStatus(targetID, finalState, msg)
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "lifecycle applied (final)", logattr.AgentID(targetID), logattr.Action(action), slog.String("state", finalState))
}

func (s *Supervisor) replyStatus(agentID, state, msg string) {
	if anyPayload, err := anypb.New(&protopkg.LifecycleStatus{
		AgentId:  agentID,
		State:    state,
		Message:  msg,
		Time:     timestamppb.Now(),
		Hostname: s.host,
	}); err == nil {
		resp := eventbus.NewEvent("system", "", eventbus.TopicAgentLifecycleStatus, "gapid", anyPayload, true)
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
		return protopkg.AgentState_AGENT_STATE_UNSPECIFIED
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
	if v := os.Getenv(product.EnvKey("PY_RUNNER")); v != "" {
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

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func actionFromEnum(act protopkg.LifecycleControl_Action) string {
	switch act {
	case protopkg.LifecycleControl_ACTION_START:
		return "start"
	case protopkg.LifecycleControl_ACTION_STOP:
		return "stop"
	case protopkg.LifecycleControl_ACTION_RELOAD:
		return "reload"
	case protopkg.LifecycleControl_ACTION_RESTART:
		return "restart"
	default:
		return "initialize"
	}
}
