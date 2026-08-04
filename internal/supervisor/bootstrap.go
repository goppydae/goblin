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
	"net"
	"strconv"
	"time"

	"github.com/hashicorp/serf/serf"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/internal/logattr"
)

// TagBootstrapExpect carries a seed node's configured cluster size
// through gossip. Peers use it to agree on the seed set without an
// operator designating one node as special.
const TagBootstrapExpect = "bootstrap_expect"

// bootstrapPollInterval is how often the seed set is re-examined while
// gossip converges. Serf reaches a stable view in well under a second
// on a LAN; this only bounds how long after the last node appears the
// cluster takes to seed.
const bootstrapPollInterval = 250 * time.Millisecond

// runBootstrapExpect blocks until the expected seed set is visible and
// then seeds the cluster from exactly one member, or returns when ctx
// ends. It is a no-op when bootstrap-expect is not configured.
//
// Every seed runs this and computes the same plan from the same
// membership view, so the election of a bootstrapper needs no
// coordination: the lowest node ID acts and the rest wait for its log.
// A disagreement about the set size is a configuration fault and is
// returned, not retried.
// joinAddr, when set, is re-attempted on every tick while the set is
// incomplete: serf's Join is one-shot, and seed nodes legitimately
// start in any order, so a single failed attempt must not strand a
// cluster that would otherwise have formed. Join is idempotent for
// peers already known.
func runBootstrapExpect(ctx context.Context, expect int, joinAddr string, membership *cluster.Membership, con *consensus.Consensus) error {
	if expect < 2 {
		return nil
	}

	local := membership.LocalNode().Name
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "waiting for bootstrap-expect peers",
		logattr.NodeID(local), slog.Int("expect", expect))

	ticker := time.NewTicker(bootstrapPollInterval)
	defer ticker.Stop()

	for {
		members := membership.Members()
		if joinAddr != "" && len(members) < expect {
			if jerr := membership.Join([]string{joinAddr}); jerr != nil {
				slog.Default().LogAttrs(ctx, slog.LevelDebug, "retrying cluster join",
					logattr.NodeID(local), logattr.Addr(joinAddr), logattr.Err(jerr))
			}
			members = membership.Members()
		}

		plan, ready, err := consensus.PlanBootstrap(expect, bootstrapPeers(members))
		if err != nil {
			return err
		}
		if ready {
			if plan.Bootstrapper != local {
				slog.Default().LogAttrs(ctx, slog.LevelInfo, "seed set complete; another node bootstraps",
					logattr.NodeID(local), slog.String("bootstrapper", plan.Bootstrapper))
				return nil
			}
			slog.Default().LogAttrs(ctx, slog.LevelInfo, "seed set complete; bootstrapping cluster",
				logattr.NodeID(local), slog.Int("servers", len(plan.Servers)))
			return con.Bootstrap(plan.Servers)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// bootstrapPeers projects the gossip view onto the planner's input.
// Only alive members count: a failed or left node cannot be part of an
// initial configuration it will never acknowledge.
func bootstrapPeers(members []serf.Member) []consensus.BootstrapPeer {
	peers := make([]consensus.BootstrapPeer, 0, len(members))
	for _, m := range members {
		if m.Status != serf.StatusAlive {
			continue
		}
		// A member's Raft address is its single advertised
		// control-plane address (GOBLIN-DIV-023); serf advertises the
		// same host:port.
		peers = append(peers, consensus.BootstrapPeer{
			NodeID: m.Name,
			Addr:   net.JoinHostPort(m.Addr.String(), strconv.Itoa(int(m.Port))),
			Expect: parseExpectTag(m.Tags[TagBootstrapExpect]),
		})
	}
	return peers
}

// parseExpectTag reads a peer's advertised seed size. An absent or
// unparsable tag reads as 0, which the planner treats as a plain
// joiner rather than a seed.
func parseExpectTag(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
