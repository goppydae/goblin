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
