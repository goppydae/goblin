// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"time"

	"github.com/goppydae/goblin/internal/logattr"
	"github.com/quic-go/quic-go"
)

// Dialling a peer that has not finished booting (GOBLIN-DIV-051).
//
// A cluster ALPN is served from Phase 4. A peer dialled before it gets
// there refuses the connection, and until this existed that refusal was
// indistinguishable from "nothing will ever serve this" - so the caller
// gave up. The observed consequence was a node joining a cluster whose
// seed had not yet registered serf-quic: the join failed once, was
// logged as a warning, and the cluster never formed a leader. The
// failure surfaced as a placement timeout in whichever suite happened to
// be running, which is why it survived as an unexplained flake across
// three of them rather than being read as a boot race.
//
// The retry lives HERE, at the dial, rather than at the join, for one
// concrete reason: this is the last layer that can still see the typed
// error. Serf aggregates join failures into a multierror of strings, so
// by the time a caller sees "Failed to join ...: Application error 0x100",
// the code is prose and errors.As has nothing to match. Retrying at the
// dial also keeps the knowledge where it belongs - "wait, the peer is
// still coming up" is a transport concern, not a membership one.

const (
	// clusterNotReadyBackoff is the pause between attempts. Short because
	// the window being covered is a phase transition on a peer that is
	// already running, not a machine boot.
	clusterNotReadyBackoff = 250 * time.Millisecond

	// refusalProbe is how long to watch a freshly dialled connection for
	// a refusal before treating it as good.
	//
	// It exists because THE REFUSAL DOES NOT ARRIVE ON THE DIAL. The
	// listener completes the QUIC handshake, inspects the negotiated
	// ALPN, and only then closes - so quic.DialAddr returns a live
	// connection and a nil error, and the application code lands on the
	// connection's context a moment later. Measured on loopback:
	// DialAddr returned in 3.08ms, the close landed 0.34ms after that.
	// This window is roughly 150x the observed gap.
	//
	// The cost is real and paid on the happy path: every dial to a peer
	// that is NOT refusing waits this long before returning. That is
	// tolerable here because both callers cache connections per peer -
	// it is once per new peer, not once per message - but it is the
	// reason this is a small number rather than a generous one.
	refusalProbe = 50 * time.Millisecond
)

// IsClusterNotReady reports whether err is a peer's cluster-not-ready
// refusal - the retryable one.
//
// It matches only the REMOTE close. A local application error with the
// same code would mean this node refused something, which is not a
// reason to redial anyone.
func IsClusterNotReady(err error) bool {
	var appErr *quic.ApplicationError
	if !errors.As(err, &appErr) {
		return false
	}
	return appErr.Remote && appErr.ErrorCode == CodeClusterNotReady
}

// dialWithClusterNotReadyRetry dials addr, retrying while the peer
// answers "cluster not ready".
//
// The budget is the CONTEXT's, deliberately: the caller already chose how
// long it is willing to wait for this peer, and a second independent
// timeout here would either contradict that choice or hide it. When the
// context expires mid-retry the last refusal is returned rather than a
// bare deadline error, because "the peer kept saying it was not ready" is
// a diagnosis and "context deadline exceeded" is not.
func dialWithClusterNotReadyRetry(
	ctx context.Context,
	addr string,
	tlsConf *tls.Config,
	quicConf *quic.Config,
) (*quic.Conn, error) {
	var lastRefusal error
	for {
		conn, err := quic.DialAddr(ctx, addr, tlsConf, quicConf)
		if err == nil {
			// Watch for a post-handshake refusal. A connection that is
			// still open when the probe expires is a real one.
			select {
			case <-conn.Context().Done():
				cause := context.Cause(conn.Context())
				if !IsClusterNotReady(cause) {
					return nil, cause
				}
				lastRefusal = cause
			case <-time.After(refusalProbe):
				return conn, nil
			}
		} else {
			if !IsClusterNotReady(err) {
				return nil, err
			}
			lastRefusal = err
		}
		slog.Default().LogAttrs(ctx, slog.LevelDebug,
			"peer is not cluster-ready; retrying dial",
			logattr.Addr(addr), logattr.Err(lastRefusal))

		select {
		case <-ctx.Done():
			// Prefer the refusal: it says WHY the wait failed.
			if lastRefusal != nil {
				return nil, lastRefusal
			}
			return nil, ctx.Err()
		case <-time.After(clusterNotReadyBackoff):
		}
	}
}
