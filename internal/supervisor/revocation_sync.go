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
	"math/rand/v2"
	"time"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// revocationSync is the anti-entropy schedule (GOBLIN-DIV-057): the
// timer that repairs revocations the best-effort delta broadcast
// dropped.
//
// Its collaborators are narrow functions rather than the membership and
// the RPC client themselves, matching publishRevocation on SchedulerRPC
// and for the same reason: the loop is then testable without a cluster,
// and a node missing either one degrades instead of panicking.
type revocationSync struct {
	revocations *capability.Revocations

	// peers returns the live nodes worth syncing with, excluding self.
	peers func() []string

	// exchange performs one round trip against a named node.
	exchange func(ctx context.Context, nodeID string, req *goblinv1.SyncRevocationsRequest) (*goblinv1.SyncRevocationsResponse, error)

	// pick chooses which of n peers to sync with this tick. Injectable so
	// the choice is bounded rather than ambient: a test can pin it, and
	// production gets a uniform draw.
	pick func(n int) int

	// interval between ticks. Zero takes the derived default.
	interval time.Duration

	logger *slog.Logger
}

// run ticks until the context is cancelled.
//
// An exchange that fails costs one tick and nothing more: a partitioned
// or dead peer is the ordinary case for a loop that exists because
// nodes go away.
func (rs *revocationSync) run(ctx context.Context) {
	interval := rs.interval
	if interval <= 0 {
		interval = capability.DefaultSyncInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.tick(ctx)
		}
	}
}

// tick is one round plus the observation that comes with it.
//
// Stats is reported here because this loop is the only thing that runs
// on a timer and already touches the filter. The geometry encodes an
// ASSUMED revocation rate, and until something reported the observed
// one the assumption was never checked against anything.
func (rs *revocationSync) tick(ctx context.Context) {
	st := rs.revocations.Stats()
	rs.logger.LogAttrs(ctx, slog.LevelDebug, "revocation filter load",
		slog.Int("current_generation", st.CurrentGeneration),
		slog.Uint64("total", st.Total),
		slog.Float64("rate_per_second", st.RatePerSecond),
		slog.Int("capacity", st.Capacity),
	)

	if err := rs.syncOnce(ctx); err != nil {
		rs.logger.LogAttrs(ctx, slog.LevelWarn,
			"revocation sync failed; this node may still accept a token a peer revoked",
			logattr.Err(err))
	}
}

// choose returns the index of this tick's peer.
func (rs *revocationSync) choose(n int) int {
	if rs.pick != nil {
		return rs.pick(n)
	}
	return rand.IntN(n)
}

// syncOnce runs a single anti-entropy round against ONE peer.
//
// One peer per tick, not the full mesh: merging is idempotent and the
// filter is a set, so convergence does not need every pair to meet on
// every tick. It keeps the cost O(1) per node per interval rather than
// O(n), which matters because the interval is short by design.
func (rs *revocationSync) syncOnce(ctx context.Context) error {
	peers := rs.peers()
	if len(peers) == 0 {
		return nil
	}
	nodeID := peers[rs.choose(len(peers))]

	resp, err := rs.exchange(ctx, nodeID, &goblinv1.SyncRevocationsRequest{
		Generations: wireFromGenerations(rs.revocations.Snapshot()),
	})
	if err != nil {
		return err
	}
	return rs.revocations.Ingest(generationsFromWire(resp.GetGenerations()))
}

// wireFromGenerations converts live generations into their wire form.
func wireFromGenerations(gens []capability.Generation) []*goblinv1.RevocationGeneration {
	out := make([]*goblinv1.RevocationGeneration, 0, len(gens))
	for _, g := range gens {
		out = append(out, &goblinv1.RevocationGeneration{
			Index:  g.Index,
			Filter: g.Filter,
		})
	}
	return out
}

// generationsFromWire converts the wire form back into generations.
func generationsFromWire(gens []*goblinv1.RevocationGeneration) []capability.Generation {
	out := make([]capability.Generation, 0, len(gens))
	for _, g := range gens {
		out = append(out, capability.Generation{
			Index:  g.GetIndex(),
			Filter: g.GetFilter(),
		})
	}
	return out
}
