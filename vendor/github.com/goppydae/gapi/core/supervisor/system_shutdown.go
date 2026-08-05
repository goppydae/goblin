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
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/goppydae/gapi/core/eventbus"
	shutdownpkg "github.com/goppydae/gapi/core/shutdown"
	"github.com/goppydae/gapi/internal/logattr"
)

// RequestShutdown records a system shutdown request; the first request
// wins and repeats are absorbed (the machine only goes down once).
func (s *Supervisor) RequestShutdown(action shutdownpkg.Action) {
	select {
	case s.shutdownReq <- action:
	default:
	}
}

// ShutdownRequests exposes the request channel; the daemon entrypoint
// selects on it to cancel the run context and complete the teardown.
func (s *Supervisor) ShutdownRequests() <-chan shutdownpkg.Action {
	return s.shutdownReq
}

// subscribeSystemShutdown wires the system.shutdown bus topic (gapictl
// shutdown [--reboot|--halt]) into the request channel. Decode fails
// closed: an unexpected payload is an error event, never a guess.
func (s *Supervisor) subscribeSystemShutdown() {
	err := s.bus.Subscribe("system", "", eventbus.TopicSystemShutdown, func(e eventbus.Event[*anypb.Any]) {
		var v wrapperspb.StringValue
		if e.Payload == nil || e.Payload.UnmarshalTo(&v) != nil {
			s.logger.LogAttrs(context.Background(), slog.LevelError, "system.shutdown with undecodable payload", logattr.EventID(e.ID))
			return
		}
		var action shutdownpkg.Action
		switch v.Value {
		case "poweroff":
			action = shutdownpkg.PowerOff
		case "reboot":
			action = shutdownpkg.Reboot
		case "halt":
			action = shutdownpkg.Halt
		default:
			s.logger.LogAttrs(context.Background(), slog.LevelError, "system.shutdown with unknown action", slog.String("action", v.Value))
			return
		}
		s.logger.LogAttrs(context.Background(), slog.LevelWarn, "system shutdown requested via bus", slog.String("action", v.Value))
		s.RequestShutdown(action)
	})
	if err != nil {
		s.logger.LogAttrs(context.Background(), slog.LevelError, "failed to subscribe to system.shutdown", logattr.Err(err))
	}
}
