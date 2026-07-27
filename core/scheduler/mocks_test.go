package scheduler

import (
	"context"
	"strings"
	"sync"

	"github.com/hashicorp/serf/serf"
)

// MockStore implements KVStore for testing. It is goroutine-safe so tests
// may exercise the scheduler concurrently (e.g. RunReconciler in a
// goroutine), mirroring the real store's concurrency contract.
type MockStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewMockStore() *MockStore {
	return &MockStore{
		data: make(map[string][]byte),
	}
}

func (m *MockStore) Set(ctx context.Context, ns, key string, val []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = val
	return nil
}

func (m *MockStore) Get(ctx context.Context, ns, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.data[key]
	return val, ok, nil
}

func (m *MockStore) Delete(ctx context.Context, ns, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MockStore) Scan(ctx context.Context, ns, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string][]byte)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

// MockCluster implements Cluster for testing
type MockCluster struct {
	members []serf.Member
}

func NewMockCluster(members []serf.Member) *MockCluster {
	return &MockCluster{members: members}
}

func (m *MockCluster) Members() []serf.Member {
	return m.members
}
