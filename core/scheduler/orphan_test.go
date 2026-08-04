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
	"sync"
	"testing"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
	"google.golang.org/protobuf/proto"
)

// recordingClient remembers every RPC it was asked to make, so a test can
// assert that the stop actually left the leader rather than that a
// function returned nil.
type recordingClient struct {
	mu    *sync.Mutex
	calls *[]string
}

func (c recordingClient) Call(method string, req, resp proto.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := ""
	if stop, ok := req.(*goblinv1.NodeStopAgentInstanceRequest); ok {
		id = stop.GetInstanceId()
	}
	*c.calls = append(*c.calls, method+" "+id)
	return nil
}

func (recordingClient) Close() error { return nil }

// newOrphanScheduler returns a scheduler whose stop RPCs are recorded,
// plus a reader for what it sent.
func newOrphanScheduler(t *testing.T) (*Scheduler, func() []string) {
	t.Helper()
	var mu sync.Mutex
	calls := []string{}
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
		{Name: "node-2", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	factory := func(addr string) (RPCClient, error) {
		return recordingClient{mu: &mu, calls: &calls}, nil
	}
	s := NewScheduler(NewMockStore(), NewMockCluster(nodes), nil, nil, factory)
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// tombstonedInstance admits an instance and terminates it, which is what
// the failover path does: the UUID is tombstoned and a replacement is
// admitted under a NEW one. The process on the original node is never
// touched, which is the whole subject of this file.
func tombstonedInstance(t *testing.T, s *Scheduler, nodeID string) string {
	t.Helper()
	ctx := context.Background()
	spec := &goblinv1.AgentSpec{Name: "spec-orphan", Replicas: 1}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	instUUID := ident.NewV7()
	if err := s.Store().Admit(ctx, spec.SpecUuid, instUUID, nodeID); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""); err != nil {
		t.Fatalf("TransitionInstance running: %v", err)
	}
	if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED, "failover"); err != nil {
		t.Fatalf("TransitionInstance terminated: %v", err)
	}
	return ident.String(instUUID)
}

// GOBLIN-DIV-067. A node that only blipped comes back running an instance
// the leader already tombstoned and replaced. Its heartbeat is the ONLY
// sighting the leader ever gets of that process - the record is gone from
// ListInstances, so no reconcile pass can see it - and the leader must
// answer it with a stop.
func TestReapOrphan_StopsTombstonedInstanceOnItsNode(t *testing.T) {
	s, sent := newOrphanScheduler(t)
	instID := tombstonedInstance(t, s, "node-1")

	orphan, err := s.ReapOrphan(context.Background(), instID, "node-1")
	if err != nil {
		t.Fatalf("ReapOrphan: %v", err)
	}
	if !orphan {
		t.Fatalf("a heartbeat for a tombstoned instance is an orphan's; ReapOrphan said it was not")
	}

	calls := sent()
	want := "NodeRPC.StopAgentInstance " + instID
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("leader did not stop the orphan on its node\n got: %v\nwant: [%s]", calls, want)
	}
}

// The common path is a live instance, and it must cost nothing: no RPC,
// and the caller is told to record the heartbeat normally.
func TestReapOrphan_LiveInstanceIsNotTouched(t *testing.T) {
	s, sent := newOrphanScheduler(t)
	_, instID := registerRunningInstance(t, s, "spec-live", "node-1")

	orphan, err := s.ReapOrphan(context.Background(), instID, "node-1")
	if err != nil {
		t.Fatalf("ReapOrphan: %v", err)
	}
	if orphan {
		t.Errorf("a running instance was reported as an orphan")
	}
	if calls := sent(); len(calls) != 0 {
		t.Errorf("a live instance's heartbeat issued RPCs: %v", calls)
	}
}

// instance.heartbeat is cluster-wide gossip, so every node sees every
// orphan. Only the leader may answer one: ungated, a three-node cluster
// would send three stops per orphan per interval, and a follower whose
// log still lags would be judging an instance from a stale tombstone
// set.
func TestReapOrphan_FollowerDoesNotReap(t *testing.T) {
	var mu sync.Mutex
	calls := []string{}
	nodes := []serf.Member{{Name: "node-1", Status: serf.StatusAlive}}
	factory := func(addr string) (RPCClient, error) {
		return recordingClient{mu: &mu, calls: &calls}, nil
	}
	leader := false
	s := NewScheduler(NewMockStore(), NewMockCluster(nodes), nil, func() bool { return leader }, factory)
	instID := tombstonedInstance(t, s, "node-1")

	orphan, err := s.ReapOrphan(context.Background(), instID, "node-1")
	if err != nil {
		t.Fatalf("ReapOrphan: %v", err)
	}
	if orphan {
		t.Errorf("a follower claimed the orphan; only the leader writes (R7)")
	}
	mu.Lock()
	sent := len(calls)
	mu.Unlock()
	if sent != 0 {
		t.Fatalf("a follower sent %d stop RPCs: %v", sent, calls)
	}
}

// The orphan's node keeps heartbeating at HeartbeatCadence until it
// complies. One stop per sighting would be a retry storm against a node
// that may simply be slow to stop the process, so repeated sightings
// inside the interval must collapse to one RPC.
func TestReapOrphan_RepeatedSightingsSendOneStop(t *testing.T) {
	s, sent := newOrphanScheduler(t)
	instID := tombstonedInstance(t, s, "node-1")

	for i := 0; i < 4; i++ {
		if _, err := s.ReapOrphan(context.Background(), instID, "node-1"); err != nil {
			t.Fatalf("ReapOrphan pass %d: %v", i, err)
		}
	}

	if calls := sent(); len(calls) != 1 {
		t.Fatalf("four sightings inside one interval sent %d stops, want 1: %v", len(calls), calls)
	}
}
