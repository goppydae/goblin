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
	"encoding/binary"
	"io"
	"log/slog"
	"strings"

	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	protopkg "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/internal/logattr"
)

// gapiMaxEnvelope bounds a single gapi-quic message. A length prefix is
// attacker-controlled, so the allocation it implies is capped before
// the read rather than after.
const gapiMaxEnvelope = 10 << 20 // 10MB

// handleQUICConn serves one gapi-quic connection, spawning a handler
// per stream.
//
// These per-connection goroutines are NOT tracked by the supervisor's
// loopGroup: the set is unbounded and short-lived by construction, and
// bounding it needs per-connection cancellation plumbing that does not
// exist. Recorded as a residual in GOBLIN-DIV-038's exit.
func handleQUICConn(conn *quic.Conn, bus eventbus.EventBus, m *cluster.Membership) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go handleQUICStream(stream, bus, m)
	}
}

// handleQUICStream reads one length-prefixed protobuf Envelope and
// routes it onto the event bus.
func handleQUICStream(stream *quic.Stream, bus eventbus.EventBus, m *cluster.Membership) {
	defer func() {
		if err := stream.Close(); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close agent event stream failed", logattr.Err(err))
		}
	}()

	// GAPI Protocol: 4-byte BigEndian length prefix + Protobuf Envelope
	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return
	}

	l := binary.BigEndian.Uint32(lenBuf[:])

	if l > gapiMaxEnvelope {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "agent event message too large", logattr.Bytes(int(l)))
		return
	}

	data := make([]byte, l)
	if _, err := io.ReadFull(stream, data); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to read agent event payload", logattr.Err(err))
		return
	}

	var env protopkg.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to unmarshal agent event envelope", logattr.Err(err))
		return
	}

	// Topic format: scope/topic or just topic
	scope := ""
	topic := env.Topic
	if i := strings.IndexByte(env.Topic, '/'); i > 0 {
		scope = env.Topic[:i]
		topic = env.Topic[i+1:]
	}

	slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "agent event received", logattr.Topic(env.Topic), logattr.Scope(scope), logattr.Source(env.Source))

	if env.Type == "event" {
		// Convert proto payload to map for EventBus
		var payloadMap map[string]interface{}
		if env.Payload != nil {
			payloadMap = make(map[string]interface{})
			// TODO: proper proto-to-map conversion
			payloadMap["raw_proto_type"] = env.Payload.TypeUrl
		}

		// Stream handler is a goroutine terminus: there is no caller to
		// propagate to, so a publish failure is logged.
		if err := bus.PublishLocal("agent", topic, payloadMap, []string{"source:" + env.Source}); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "publish failed", logattr.Topic(topic), logattr.Source(env.Source), logattr.Err(err))
		}
	}
}
