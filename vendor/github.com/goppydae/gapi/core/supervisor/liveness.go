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
	"log/slog"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/core/version"
	"github.com/goppydae/gapi/internal/logattr"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// registerLivenessHandlers subscribes the handlers that answer for the
// PROCESS rather than for the agent set, so they are reachable the
// moment the transport is.
//
// GAPI-DIV-120. The QUIC listener is accepting at about 0.2s while
// setupAgents runs for as long as the agents take, and until this
// subscription exists the daemon ACCEPTS CONNECTIONS WITH NO PING
// SUBSCRIBER. core/client published ping once, fire-and-forget, so a
// probe landing in that window was dropped and the client waited out its
// whole deadline for a pong that could never arrive.
//
// MEASURED on private listen addresses, 4 of 4 runs: launch to first
// pong was 0.21s with no agents and 30.21s with the ADK fixtures -
// exactly gapictl's 30s timeout rather than anything about the agents.
// Whichever side of that race won decided the outcome, and CI load moves
// agent startup rather than the client, so the failure flipped
// discontinuously instead of degrading. That is why it read as a second
// mode 26 standard deviations out rather than a slow tail, and why
// raising the timeout would only have bought a slower red.
//
// THE DISCRIMINATOR FOR ADDING ANYTHING HERE is whether the answer
// depends on the registry. If it does, it belongs in registerHandlers:
// answering early from a registry that is still filling is a WRONG
// answer rather than an early one, and that trades a timeout for a
// confidently partial reply, which is worse and harder to see. Ping is
// the whole of it today.
func (s *Supervisor) registerLivenessHandlers() {
	err := s.bus.SubscribePrefix("system", "", "ping", func(e eventbus.Event[*anypb.Any]) {
		s.logger.LogAttrs(context.Background(), slog.LevelDebug, "received ping, preparing pong",
			logattr.Event("handling_ping"), logattr.EventID(e.ID))
		s.logger.LogAttrs(context.Background(), slog.LevelInfo, "lifecycle event",
			logattr.Event("lifecycle"), logattr.Source("supervisor"), logattr.Action("handle_ping"),
			logattr.AgentID("supervisor"), logattr.Version(version.BinaryVersion()))

		pong := &protopkg.PingStatus{Status: "pong"}
		anyPayload, err := anypb.New(pong)
		if err != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to pack pong payload", logattr.Err(err))
			return
		}

		response := eventbus.NewEvent("system", "", "pong", "gapid", anyPayload)
		response.ID = e.ID // correlate reply to the originating request
		_ = s.bus.Publish(response)
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to ping event", logattr.Err(err))
	}
}
