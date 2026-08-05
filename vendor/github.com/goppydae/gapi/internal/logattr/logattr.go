// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package logattr holds gapi's typed log attribute constructors: each
// pins one key to one value type, so a dropped value, swapped pair, or
// wrong type is a compile error instead of a quietly wrong JSON field
// (go-manifesto section 8). Add new keys here, never inline at call
// sites. Key vocabulary is shared by convention with goblin's
// internal/logattr - same concept, same key string (agent_id, topic,
// err) - so cross-repo log queries line up.
package logattr

import (
	"log/slog"
	"time"
)

func Module(s string) slog.Attr    { return slog.String("module", s) }
func Component(s string) slog.Attr { return slog.String("component", s) }
func AgentID(s string) slog.Attr   { return slog.String("agent_id", s) }
func Path(s string) slog.Attr      { return slog.String("path", s) }
func PathType(s string) slog.Attr  { return slog.String("path_type", s) }
func Event(s string) slog.Attr     { return slog.String("event", s) }
func EventID(s string) slog.Attr   { return slog.String("event_id", s) }
func Topic(s string) slog.Attr     { return slog.String("topic", s) }
func Scope(s string) slog.Attr     { return slog.String("scope", s) }
func Source(s string) slog.Attr    { return slog.String("source", s) }
func Action(s string) slog.Attr    { return slog.String("action", s) }
func Version(s string) slog.Attr   { return slog.String("version", s) }
func Addr(s string) slog.Attr      { return slog.String("addr", s) }
func Host(s string) slog.Attr      { return slog.String("host", s) }
func KeyPath(s string) slog.Attr   { return slog.String("key_path", s) }
func Reason(s string) slog.Attr    { return slog.String("reason", s) }
func Count(n int) slog.Attr        { return slog.Int("count", n) }

func Signal(s string) slog.Attr { return slog.String("signal", s) }
func Type(s string) slog.Attr   { return slog.String("type", s) }
func Lang(s string) slog.Attr   { return slog.String("lang", s) }
func Hash(s string) slog.Attr   { return slog.String("hash", s) }
func Bytes(n int) slog.Attr     { return slog.Int("bytes", n) }

func RetryIn(d time.Duration) slog.Attr { return slog.Duration("retry_in", d) }
func Dependency(s string) slog.Attr     { return slog.String("dependency", s) }

// PayloadType records the Go type of an event payload (%T), not the
// payload itself - payloads may carry secrets.
func PayloadType(s string) slog.Attr { return slog.String("payload_type", s) }

// MissingDependency names the unmet requirement that blocked a start.
func MissingDependency(s string) slog.Attr { return slog.String("missing_dependency", s) }

// Err pins the conventional "err" key; nil-safe for termini that log
// unconditionally.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("err", "")
	}
	return slog.String("err", err.Error())
}
