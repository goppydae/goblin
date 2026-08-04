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
	"fmt"
	"log/slog"

	gapiclock "github.com/goppydae/gapi/core/clock"
	gapieventbus "github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/goblin/internal/logattr"
)

// phaseNetworkGate runs Phase 3: block until a local agent publishes
// network readiness, or fail loudly on expiry (GOBLIN-DIV-011, R13).
//
// This now runs BEFORE cluster join, which is what the architecture doc
// always claimed and the code did not do. It used to sit after Serf
// membership, Raft consensus and the scheduler were already built, so a
// gate expiry aborted a node that had already joined gossip and
// possibly raft. Failing here leaves no cluster state behind.
//
// The gate has NO PRODUCER today. Phase 2 - the topological start of
// local agents - does not exist, so nothing on this node ever publishes
// the readiness topic and the only agents that run are the instances
// the cluster scheduler places, which arrive after this point. A
// nonzero NetworkGateTimeout therefore always expires. That is
// GOBLIN-DIV-050, not a defect in this gate: the gate is correct and
// correctly positioned, and it starts working the moment something
// starts a network agent locally.
//
// Zero disables it, which is the default and why this is not a
// production outage today.
func (s *Supervisor) phaseNetworkGate(ctx context.Context, st *runState) error {
	if s.cfg.NetworkGateTimeout <= 0 {
		return nil
	}
	slog.Default().LogAttrs(ctx, slog.LevelInfo, "waiting for network agent",
		logattr.Topic(gapieventbus.TopicAgentNetworkRunning),
		slog.Duration("timeout", s.cfg.NetworkGateTimeout))

	if err := st.localBus.WaitForTopic(ctx, "system", "", gapieventbus.TopicAgentNetworkRunning,
		s.cfg.NetworkGateTimeout, gapiclock.RealClock{}); err != nil {
		return fmt.Errorf("network-readiness gate: %w (no %s event within %s)",
			err, gapieventbus.TopicAgentNetworkRunning, s.cfg.NetworkGateTimeout)
	}
	return nil
}
