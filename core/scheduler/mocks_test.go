package scheduler

import (
	"context"
	"strings"

	"github.com/hashicorp/serf/serf"
)

// MockStore implements KVStore for testing
type MockStore struct {
	data map[string][]byte
}

func NewMockStore() *MockStore {
	return &MockStore{
		data: make(map[string][]byte),
	}
}

func (m *MockStore) Set(ctx context.Context, ns, key string, val []byte) error {
	m.data[key] = val
	return nil
}

func (m *MockStore) Get(ctx context.Context, ns, key string) ([]byte, bool, error) {
	val, ok := m.data[key]
	return val, ok, nil
}

func (m *MockStore) Delete(ctx context.Context, ns, key string) error {
	delete(m.data, key)
	return nil
}

func (m *MockStore) Scan(ctx context.Context, ns, prefix string) (map[string][]byte, error) {
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
