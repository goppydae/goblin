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
	"errors"
	"testing"
	"time"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// peerExchange answers as a real peer would: it runs the request
// through the actual handler against that peer's filter. Mocking the
// merge would test the mock, and the merge is the whole mechanism.
func peerExchange(peer *capability.Revocations, dialled *[]string) func(context.Context, string, *goblinv1.SyncRevocationsRequest) (*goblinv1.SyncRevocationsResponse, error) {
	return func(_ context.Context, nodeID string, req *goblinv1.SyncRevocationsRequest) (*goblinv1.SyncRevocationsResponse, error) {
		*dialled = append(*dialled, nodeID)
		var resp goblinv1.SyncRevocationsResponse
		if err := (&SchedulerRPC{revocations: peer}).SyncRevocations(req, &resp); err != nil {
			return nil, err
		}
		return &resp, nil
	}
}

// TestRevocationSync_OneTickRepairsBothNodes is the anti-entropy round
// itself: the exchange is symmetric, so a single tick against a single
// peer repairs the caller and the responder alike.
func TestRevocationSync_OneTickRepairsBothNodes(t *testing.T) {
	local := capability.NewRevocations()
	mine := ident.NewV7()
	local.Revoke(mine)

	peer := capability.NewRevocations()
	theirs := ident.NewV7()
	peer.Revoke(theirs)

	var dialled []string
	rs := &revocationSync{
		revocations: local,
		peers:       func() []string { return []string{"node-b"} },
		exchange:    peerExchange(peer, &dialled),
		logger:      quietLogger(),
	}

	if err := rs.syncOnce(context.Background()); err != nil {
		t.Fatalf("syncOnce: %v", err)
	}

	if !local.IsRevoked(theirs) {
		t.Error("the peer's revocation was not merged locally: this node still accepts it")
	}
	if !peer.IsRevoked(mine) {
		t.Error("this node's revocation never reached the peer: the exchange repaired one direction only")
	}
	if len(dialled) != 1 {
		t.Errorf("dialled %d peers in one tick, want exactly 1: the cost must stay O(1) per node", len(dialled))
	}
}

// TestRevocationSync_SpreadsAcrossPeers keeps one peer per tick from
// meaning the SAME peer every tick. Convergence relies on every pair
// meeting eventually; a fixed choice leaves a third node repaired only
// by whoever happens to pick it, which on a fixed choice is nobody.
//
// Bounded rather than merely random: with three peers over fifty ticks,
// seeing only one distinct peer has probability 2*(1/3)^49. A failure
// here is a defect, not a bad day.
func TestRevocationSync_SpreadsAcrossPeers(t *testing.T) {
	peer := capability.NewRevocations()
	var dialled []string

	rs := &revocationSync{
		revocations: capability.NewRevocations(),
		peers:       func() []string { return []string{"node-a", "node-b", "node-c"} },
		exchange:    peerExchange(peer, &dialled),
		logger:      quietLogger(),
	}

	for range 50 {
		if err := rs.syncOnce(context.Background()); err != nil {
			t.Fatalf("syncOnce: %v", err)
		}
	}

	distinct := map[string]struct{}{}
	for _, d := range dialled {
		distinct[d] = struct{}{}
	}
	if len(distinct) < 2 {
		t.Errorf("fifty ticks reached %d distinct peers, want all 3: a fixed choice never repairs the others", len(distinct))
	}
}

// failingSync builds a loop whose every exchange fails, and reports
// each attempt on the returned channel.
func failingSync(t *testing.T) (*revocationSync, chan struct{}) {
	t.Helper()
	attempts := make(chan struct{}, 16)
	return &revocationSync{
		revocations: capability.NewRevocations(),
		peers:       func() []string { return []string{"unreachable"} },
		exchange: func(context.Context, string, *goblinv1.SyncRevocationsRequest) (*goblinv1.SyncRevocationsResponse, error) {
			select {
			case attempts <- struct{}{}:
			default:
			}
			return nil, errors.New("dial failed")
		},
		pick:     func(int) int { return 0 },
		interval: time.Millisecond,
		logger:   quietLogger(),
	}, attempts
}

// TestRevocationSync_KeepsRunningAfterAPeerFails. A partitioned or dead
// peer is the ordinary case for this loop - it exists because nodes go
// away - so an exchange that fails must cost one tick, not the loop.
func TestRevocationSync_KeepsRunningAfterAPeerFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rs, attempts := failingSync(t)
	go rs.run(ctx)

	for i := range 3 {
		select {
		case <-attempts:
		case <-time.After(5 * time.Second):
			t.Fatalf("the loop stopped after %d failed exchanges; one unreachable peer must not end anti-entropy", i)
		}
	}
}

// TestRevocationSync_StopsWhenCancelled pairs with the spawn in the
// serving phase: the loop is joined at shutdown, so it has to return.
func TestRevocationSync_StopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rs, _ := failingSync(t)

	done := make(chan struct{})
	go func() {
		rs.run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after cancellation; shutdown would hang joining it")
	}
}
