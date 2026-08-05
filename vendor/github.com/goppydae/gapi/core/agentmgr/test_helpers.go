// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

import (
	"context"
)

// ==================== Mock DependencyResolver ====================

type MockDependencyResolver struct {
	dependencies map[string][]string
	states       map[string]bool
}

func NewMockDependencyResolver() *MockDependencyResolver {
	return &MockDependencyResolver{
		dependencies: make(map[string][]string),
		states:       make(map[string]bool),
	}
}

func (m *MockDependencyResolver) DepsOf(id string) []string {
	if deps, ok := m.dependencies[id]; ok {
		return deps
	}
	return nil
}

func (m *MockDependencyResolver) IsRunning(id string) bool {
	if state, ok := m.states[id]; ok {
		return state
	}
	return false
}

func (m *MockDependencyResolver) EnsureStarted(ctx context.Context, id string) error {
	m.states[id] = true
	return nil
}

func (m *MockDependencyResolver) SetDeps(id string, deps []string) {
	m.dependencies[id] = deps
}

func (m *MockDependencyResolver) SetRunning(id string, running bool) {
	m.states[id] = running
}
