// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	gapiclock "github.com/goppydae/gapi/core/clock"
	"github.com/goppydae/goblin/internal/logattr"
)

const (
	// HeartbeatCadence is the node-side publish interval for instance
	// heartbeats. The policy constant lives with its consumer (the
	// reconciler); the kernel bus is only the transport.
	HeartbeatCadence = 5 * time.Second

	// missedHeartbeatLimit is how many consecutive cadences a running
	// instance may miss before the reconciler declares it failed.
	missedHeartbeatLimit = 3
)

// staleAfter is the silence window after which a running instance with
// no heartbeat is considered dead.
const staleAfter = time.Duration(missedHeartbeatLimit) * HeartbeatCadence

type heartbeat struct {
	nodeID string
	state  string
	at     time.Time
}

// SetClock replaces the scheduler's clock; tests inject a MockClock to
// drive staleness deterministically.
func (s *Scheduler) SetClock(clk gapiclock.Clock) {
	s.clk = clk
}

func (s *Scheduler) now() time.Time {
	if s.clk == nil {
		return time.Now()
	}
	return s.clk.Now()
}

// ObserveHeartbeat records a node's report of an instance's state. The
// supervisor feeds this from the distributed bus; the reconciler reads
// it to decide instance health. Heartbeat state is leader-local and
// in-memory by design: a new leader rebuilds it from live traffic,
// protected by the leader grace below.
func (s *Scheduler) ObserveHeartbeat(instanceID, nodeID, state string, at time.Time) {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if s.heartbeats == nil {
		s.heartbeats = make(map[string]heartbeat)
	}
	s.heartbeats[instanceID] = heartbeat{nodeID: nodeID, state: state, at: at}
}

// forgetHeartbeat drops a replaced instance's record so its id cannot
// shadow a future observation.
func (s *Scheduler) forgetHeartbeat(instanceID string) {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	delete(s.heartbeats, instanceID)
	delete(s.pendingSince, instanceID)
	delete(s.locators, instanceID)
}

// pendingStale reports whether a pending instance has waited past the
// staleness window without its node ever reporting it: a dispatch that
// hung or died (e.g. leader loss mid-dispatch) must not hold a replica
// slot forever. First sight starts the clock.
func (s *Scheduler) pendingStale(instanceID string) bool {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if _, seen := s.heartbeats[instanceID]; seen {
		return false // its node is reporting it; the running path judges it
	}
	if s.pendingSince == nil {
		s.pendingSince = make(map[string]time.Time)
	}
	first, ok := s.pendingSince[instanceID]
	if !ok {
		s.pendingSince[instanceID] = s.now()
		return false
	}
	return s.now().Sub(first) >= staleAfter
}

// noteReconcileBaseline records when this scheduler last began acting as
// leader. Instances are never declared stale before baseline+staleAfter:
// a fresh leader has heard nothing yet, and silence it caused is not
// evidence of instance death.
func (s *Scheduler) noteReconcileBaseline() {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if s.leadSince.IsZero() {
		s.leadSince = s.now()
	}
}

// clearReconcileBaseline resets the grace when leadership is lost, so
// regaining it starts a fresh window.
func (s *Scheduler) clearReconcileBaseline() {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	s.leadSince = time.Time{}
}

// instanceUnhealthy reports whether a running instance should be treated
// as dead: its node reported it failed, or it has been silent past the
// staleness window (and the leader grace has elapsed).
func (s *Scheduler) instanceUnhealthy(instanceID string) bool {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()

	now := s.now()
	hb, seen := s.heartbeats[instanceID]
	if seen && hb.state == "failed" {
		return true
	}

	graceOver := !s.leadSince.IsZero() && now.Sub(s.leadSince) >= staleAfter
	if !graceOver {
		return false
	}
	if !seen {
		return true // grace elapsed and the instance has never reported
	}
	return now.Sub(hb.at) >= staleAfter
}

// orphanStopInterval bounds how often one orphan is asked to stop. Its
// node keeps reporting it every HeartbeatCadence until the process is
// gone, and a stop RPC per sighting would be a retry storm against a
// node that may simply be slow to comply.
const orphanStopInterval = staleAfter

// noteOrphanStop reports whether enough time has passed to ask about
// this orphan again, recording the attempt when it says yes.
func (s *Scheduler) noteOrphanStop(instanceID string) bool {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	if s.orphanStops == nil {
		s.orphanStops = make(map[string]time.Time)
	}
	now := s.now()
	if last, asked := s.orphanStops[instanceID]; asked && now.Sub(last) < orphanStopInterval {
		return false
	}
	s.orphanStops[instanceID] = now
	return true
}

// ReapOrphan answers a heartbeat for an instance the cluster has already
// tombstoned by stopping it on the node still reporting it. It reports
// whether the heartbeat was an orphan's, in which case the caller must
// NOT record it: the UUID is dead and feeding it to the health view
// would resurrect a tombstoned identity in the leader's memory.
//
// GOBLIN-DIV-067, reconcile-on-reconnect. Failover tombstones the dead
// UUID and admits a REPLACEMENT under a new one (reconciler.go:86,
// :150), and nothing ever stopped the original process. A node that
// only blipped past staleAfter therefore came back running a second
// live copy, and this heartbeat is the ONLY sighting the leader gets of
// it - the record is gone from ListInstances, so no reconcile pass can
// find it by walking state.
//
// Kubernetes bounds this by refusing to place the replacement at all
// (StatefulSet at-most-one); Nomad places it and picks a winner when
// the node returns (disconnect.reconcile). This is the latter, fixed at
// keep_replacement: the replacement is already running and already
// healthy, so the returning copy is the one that loses.
//
// LEADER-ONLY, and that gate is load-bearing rather than an
// optimisation: instance.heartbeat is cluster-wide gossip, so every
// node sees every orphan. Ungated, an orphan would draw one stop RPC
// per node per interval, and a follower whose log still lags would be
// deciding an instance's fate from a stale tombstone set. Only the
// leader writes (review R7); the reap is a write.
//
// Cheap on the common path - one predicate and one map lookup, and no
// RPC unless the UUID is tombstoned.
func (s *Scheduler) ReapOrphan(ctx context.Context, instanceID, nodeID string) (bool, error) {
	if instanceID == "" || !s.leading() || !s.store.IsTombstoned(instanceID) {
		return false, nil
	}
	if !s.noteOrphanStop(instanceID) {
		return true, nil
	}
	slog.Default().LogAttrs(ctx, slog.LevelWarn,
		"node is reporting a tombstoned instance; stopping the orphan",
		logattr.InstanceID(instanceID), logattr.NodeID(nodeID))
	if err := s.stopAgentOnNode(ctx, nodeID, instanceID); err != nil {
		return true, fmt.Errorf("stop orphaned instance %s on %s: %w", instanceID, nodeID, err)
	}
	return true, nil
}

// KickReconcile requests an immediate reconcile pass; RunReconciler
// coalesces kicks with its ticker. Non-blocking: a pending kick absorbs
// further ones.
func (s *Scheduler) KickReconcile() {
	select {
	case s.reconcileKick <- struct{}{}:
	default:
	}
}
