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
	"testing"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

func TestAgentStorage(t *testing.T) {
	mockStore := NewMockStore()
	s := NewScheduler(mockStore, nil, nil, nil, nil) // Cluster/Bus not needed for storage tests
	ctx := context.Background()

	// 1. Test Register and Get: registration mints the spec UUID; the
	// spec is retrievable by both UUID and name.
	spec := &goblinv1.AgentSpec{
		Name:     "agent-1",
		Type:     "python-trader",
		Replicas: 3,
		Resources: &goblinv1.ResourceReq{
			Cpu:    1.5,
			Memory: 1024,
		},
	}

	if err := s.RegisterAgent(ctx, spec); err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if len(spec.SpecUuid) != 16 {
		t.Fatalf("RegisterAgent did not mint a 16-byte spec UUID, got %d bytes", len(spec.SpecUuid))
	}

	byName, err := s.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent by name failed: %v", err)
	}
	byUUID, err := s.GetAgent(ctx, ident.String(spec.SpecUuid))
	if err != nil {
		t.Fatalf("GetAgent by UUID failed: %v", err)
	}
	for _, retrieved := range []*goblinv1.AgentSpec{byName, byUUID} {
		if ident.String(retrieved.SpecUuid) != ident.String(spec.SpecUuid) {
			t.Errorf("Expected UUID %s, got %s", ident.String(spec.SpecUuid), ident.String(retrieved.SpecUuid))
		}
		if retrieved.Replicas != spec.Replicas {
			t.Errorf("Expected Replicas %d, got %d", spec.Replicas, retrieved.Replicas)
		}
		if retrieved.Resources.Cpu != spec.Resources.Cpu {
			t.Errorf("Expected CPU %f, got %f", spec.Resources.Cpu, retrieved.Resources.Cpu)
		}
	}

	// Re-registering the same name must reuse the identity, not mint a
	// duplicate spec.
	update := &goblinv1.AgentSpec{Name: "agent-1", Type: "python-trader", Replicas: 5}
	if err := s.RegisterAgent(ctx, update); err != nil {
		t.Fatalf("RegisterAgent update failed: %v", err)
	}
	if ident.String(update.SpecUuid) != ident.String(spec.SpecUuid) {
		t.Errorf("re-register minted a new UUID: %s != %s", ident.String(update.SpecUuid), ident.String(spec.SpecUuid))
	}

	// 2. Test List
	spec2 := &goblinv1.AgentSpec{
		Name: "agent-2",
		Type: "logger",
	}
	if err := s.RegisterAgent(ctx, spec2); err != nil {
		t.Fatalf("RegisterAgent 2 failed: %v", err)
	}

	list, err := s.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Expected 2 agents, got %d", len(list))
	}

	// 3. Test Delete (by name)
	if err := s.DeleteAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}

	if _, err := s.GetAgent(ctx, "agent-1"); err == nil {
		t.Error("Expected error getting deleted agent, got nil")
	}

	// A nameless spec must be rejected.
	if err := s.RegisterAgent(ctx, &goblinv1.AgentSpec{Type: "x"}); err == nil {
		t.Error("RegisterAgent accepted a spec with no name")
	}
}

func TestInstanceStorage(t *testing.T) {
	mockStore := NewMockStore()
	s := NewScheduler(mockStore, nil, nil, nil, nil)
	ctx := context.Background()

	specA := ident.NewV7()
	specB := ident.NewV7()

	instUUID := ident.NewV7()

	// 1. Admit, transition to running, and Get
	if err := s.Store().Admit(ctx, specA, instUUID, "node-1"); err != nil {
		t.Fatalf("Admit failed: %v", err)
	}
	if err := s.Store().TransitionInstance(ctx, instUUID, goblinv1.InstanceState_INSTANCE_STATE_RUNNING, ""); err != nil {
		t.Fatalf("TransitionInstance failed: %v", err)
	}

	got, err := s.GetInstance(ctx, ident.String(instUUID))
	if err != nil {
		t.Fatalf("GetInstance failed: %v", err)
	}
	if ident.String(got.SpecUuid) != ident.String(specA) {
		t.Errorf("Expected spec UUID %s, got %s", ident.String(specA), ident.String(got.SpecUuid))
	}
	if got.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		t.Errorf("Expected state RUNNING, got %v", got.State)
	}

	// An admission without a valid UUID must be rejected.
	if err := s.Store().Admit(ctx, specA, nil, "node-1"); err == nil {
		t.Error("Admit accepted an instance with no UUID")
	}

	// 2. List with Filter
	if err := s.Store().Admit(ctx, specA, ident.NewV7(), "node-2"); err != nil {
		t.Fatalf("Admit 2 failed: %v", err)
	}
	if err := s.Store().Admit(ctx, specB, ident.NewV7(), "node-1"); err != nil { // Different spec
		t.Fatalf("Admit 3 failed: %v", err)
	}

	// Filter by spec A
	list, err := s.ListInstances(ctx, ident.String(specA))
	if err != nil {
		t.Fatalf("ListInstances failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("Expected 2 instances for spec A, got %d", len(list))
	}

	// No filter
	all, err := s.ListInstances(ctx, "")
	if err != nil {
		t.Fatalf("ListInstances(all) failed: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("Expected 3 total instances, got %d", len(all))
	}
}
