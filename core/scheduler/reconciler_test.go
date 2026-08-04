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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
)

// scaleUpFixture builds a 2-node cluster and a spec wanting 3 replicas.
// factory decides which dispatch path the test exercises: a working
// factory reaches RUNNING, nil fails every dispatch and reaches
// TERMINATED.
func scaleUpFixture(t *testing.T, name string, factory ClientFactory) (*Scheduler, *goblinv1.AgentSpec) {
	t.Helper()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
		{Name: "node-2", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	s := NewScheduler(NewMockStore(), NewMockCluster(nodes), nil, nil, factory)
	spec := &goblinv1.AgentSpec{
		Name:      name,
		Replicas:  3,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(context.Background(), spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return s, spec
}

// settledInstances joins the scheduler's in-flight dispatches, then reads
// the instances once and asserts every one has reached state.
//
// A read taken straight after ReconcileAgents used to race the dispatch
// goroutines: the original single-read test failed 22 of 50 local runs and
// once in CI, always finding an instance already moved past the state it
// asserted. AwaitDispatches is a real join point, so this needs no
// polling and no deadline guessing - the states it reads are final.
func settledInstances(t *testing.T, s *Scheduler, specID string, want int, state goblinv1.InstanceState) []*goblinv1.AgentInstance {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.AwaitDispatches(ctx); err != nil {
		t.Fatalf("AwaitDispatches: %v", err)
	}
	instances, err := s.ListInstances(ctx, specID)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != want {
		t.Fatalf("got %d instances, want %d", len(instances), want)
	}
	for _, inst := range instances {
		if inst.State != state {
			t.Errorf("instance %s in state %v, want %v", ident.String(inst.InstanceUuid), inst.State, state)
		}
	}
	return instances
}

// TestReconcileScaleUpRunsReplicas covers the path an operator cares
// about: a spec short of its replica count is filled, and every instance
// reaches RUNNING once its node accepts the dispatch.
func TestReconcileScaleUpRunsReplicas(t *testing.T) {
	s, spec := scaleUpFixture(t, "agent-scale", NewMockClientFactory())

	if err := s.ReconcileAgents(context.Background()); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}

	instances := settledInstances(t, s, ident.String(spec.SpecUuid), 3, goblinv1.InstanceState_INSTANCE_STATE_RUNNING)

	// Placement must spread across both nodes rather than stack all
	// three on one, which the resource request leaves room for.
	perNode := map[string]int{}
	for _, inst := range instances {
		perNode[inst.NodeId]++
	}
	if len(perNode) != 2 {
		t.Errorf("expected 3 replicas spread over 2 nodes, got %v", perNode)
	}
}

// TestReconcileScaleUpTerminatesFailedDispatch covers the path the old
// single test was actually exercising by accident, through a nil client
// factory, and never asserted: a dispatch failure must not leave a
// pending ghost, so the instance is terminated for the next reconcile to
// re-place (reconciler.go createInstance).
func TestReconcileScaleUpTerminatesFailedDispatch(t *testing.T) {
	s, spec := scaleUpFixture(t, "agent-scale-nodispatch", nil)

	if err := s.ReconcileAgents(context.Background()); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}

	instances := settledInstances(t, s, ident.String(spec.SpecUuid), 3, goblinv1.InstanceState_INSTANCE_STATE_TERMINATED)
	for _, inst := range instances {
		if !strings.HasPrefix(inst.Reason, "dispatch failed:") {
			t.Errorf("instance %s terminated with reason %q, want a dispatch-failed reason", ident.String(inst.InstanceUuid), inst.Reason)
		}
	}
}

func TestReconcileScaleDown(t *testing.T) {
	mockStore := NewMockStore()
	mockCluster := NewMockCluster(nil) // Cluster not needed for downscaling if logic is simple
	s := NewScheduler(mockStore, mockCluster, nil, nil, nil)
	ctx := context.Background()

	// 1. Register Spec (Replicas=1)
	spec := &goblinv1.AgentSpec{Name: "agent-down", Replicas: 1}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	// 2. Create 3 existing running instances
	for i := 0; i < 3; i++ {
		instUUID := ident.NewV7()
		if err := s.Store().Admit(ctx, spec.SpecUuid, instUUID, "n1"); err != nil {
			t.Fatalf("Admit %d: %v", i, err)
		}
		if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""); err != nil {
			t.Fatalf("TransitionInstance %d: %v", i, err)
		}
	}

	// 3. Reconcile twice: pass 1 terminates the excess (tombstoning
	// them), pass 2 archives the terminated records.
	for i := 0; i < 2; i++ {
		if err := s.ReconcileAgents(ctx); err != nil {
			t.Fatalf("ReconcileAgents failed: %v", err)
		}
	}

	// 4. Verify count = 1
	instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	if len(instances) != 1 {
		t.Errorf("Expected 1 instance after scale down, got %d", len(instances))
	}
}

// TestRunReconcilerLeaderGate verifies R7: only the leader reconciles.
// Followers must skip ticks entirely; a leadership flip mid-run starts
// reconciliation without a restart.
func TestRunReconcilerLeaderGate(t *testing.T) {
	mockStore := NewMockStore()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	mockCluster := NewMockCluster(nodes)

	var leader atomic.Bool // starts false: follower
	s := NewScheduler(mockStore, mockCluster, nil, func() bool { return leader.Load() }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spec := &goblinv1.AgentSpec{
		Name:      "agent-gated",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	go s.RunReconciler(ctx, 5*time.Millisecond)

	// Follower: several tick intervals pass, nothing may be scheduled.
	time.Sleep(50 * time.Millisecond)
	if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) != 0 {
		t.Fatalf("follower reconciled: %d instances created, want 0", len(instances))
	}

	// Become leader: reconciliation must start without a restart.
	leader.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid))
	t.Fatalf("leader did not reconcile: %d instances, want 1", len(instances))
}

// TestNewSchedulerNilLeaderGate verifies standalone semantics: a nil
// predicate means always-leader (single-node mode).
func TestNewSchedulerNilLeaderGate(t *testing.T) {
	mockStore := NewMockStore()
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	s := NewScheduler(mockStore, NewMockCluster(nodes), nil, nil, nil)
	ctx := context.Background()

	spec := &goblinv1.AgentSpec{
		Name:      "agent-standalone",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if err := s.ReconcileAgents(ctx); err != nil {
		t.Fatalf("ReconcileAgents failed: %v", err)
	}
	if instances, _ := s.ListInstances(ctx, ident.String(spec.SpecUuid)); len(instances) != 1 {
		t.Fatalf("standalone reconcile: %d instances, want 1", len(instances))
	}
}

// TestRunReconcilerDrainsInFlightDispatches pins the lifecycle contract:
// the reconciler loop owns the dispatch goroutines it starts, so it must
// not return while one is still running.
//
// Before the drain existed, createInstance fired an unsupervised
// goroutine whose only bound was ctx cancellation, so RunReconciler
// returned while dispatches were mid-flight and the supervisor had no way
// to know they were gone. Reverting the drain makes this test fail at the
// "returned with a dispatch still in flight" assertion.
func TestRunReconcilerDrainsInFlightDispatches(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	nodes := []serf.Member{
		{Name: "node-1", Status: serf.StatusAlive, Tags: map[string]string{"cpu": "4", "memory": "8192"}},
	}
	s := NewScheduler(NewMockStore(), NewMockCluster(nodes), nil, nil,
		NewBlockingClientFactory(entered, release))

	spec := &goblinv1.AgentSpec{
		Name:      "agent-drain",
		Replicas:  1,
		Resources: &goblinv1.ResourceReq{Cpu: 0.1, Memory: 10},
	}
	if err := s.RegisterAgent(context.Background(), spec); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		s.RunReconciler(ctx, 5*time.Millisecond)
		close(returned)
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no dispatch was started")
	}

	// Stop the loop with the dispatch still parked in Call.
	cancel()

	select {
	case <-returned:
		t.Fatal("RunReconciler returned with a dispatch still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("RunReconciler did not return after the dispatch finished")
	}
}
