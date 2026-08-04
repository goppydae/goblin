// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package scheduler

import (
	"time"

	gapiclock "github.com/goppydae/gapi/core/clock"
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

// KickReconcile requests an immediate reconcile pass; RunReconciler
// coalesces kicks with its ticker. Non-blocking: a pending kick absorbs
// further ones.
func (s *Scheduler) KickReconcile() {
	select {
	case s.reconcileKick <- struct{}{}:
	default:
	}
}
