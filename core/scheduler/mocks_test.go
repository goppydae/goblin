package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/serf/serf"
	"google.golang.org/protobuf/proto"
)

// MockStore implements KVStore for testing. It is goroutine-safe so tests
// may exercise the scheduler concurrently (e.g. RunReconciler in a
// goroutine), mirroring the real store's concurrency contract - and it
// mirrors the FSM's admission/transition/tombstone rules via the same
// exported consensus.LegalTransition the FSM uses.
type MockStore struct {
	mu         sync.Mutex
	data       map[string][]byte
	instances  map[string]*goblinv1.AgentInstance
	tombstones map[string]struct{}
}

func NewMockStore() *MockStore {
	return &MockStore{
		data:       make(map[string][]byte),
		instances:  make(map[string]*goblinv1.AgentInstance),
		tombstones: make(map[string]struct{}),
	}
}

func (m *MockStore) Admit(ctx context.Context, specUUID, instanceUUID []byte, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ident.String(instanceUUID)
	if id == "" || ident.String(specUUID) == "" || nodeID == "" {
		return fmt.Errorf("mock admit: malformed admission")
	}
	if _, dead := m.tombstones[id]; dead {
		return fmt.Errorf("mock admit: %s tombstoned", id)
	}
	if _, exists := m.instances[id]; exists {
		return fmt.Errorf("mock admit: %s already admitted", id)
	}
	m.instances[id] = &goblinv1.AgentInstance{
		InstanceUuid: append([]byte(nil), instanceUUID...),
		SpecUuid:     append([]byte(nil), specUUID...),
		NodeId:       nodeID,
		State:        goblinv1.InstanceState_INSTANCE_STATE_ADMITTED,
	}
	return nil
}

func (m *MockStore) TransitionInstance(ctx context.Context, instanceUUID []byte, to goblinv1.InstanceState, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ident.String(instanceUUID)
	inst, ok := m.instances[id]
	if !ok {
		return fmt.Errorf("mock transition: unknown instance %s", id)
	}
	if !consensus.LegalTransition(inst.State, to) {
		return fmt.Errorf("%w: %s -> %s", consensus.ErrIllegalTransition, inst.State, to)
	}
	inst.State = to
	if reason != "" {
		inst.Reason = reason
	}
	switch to {
	case goblinv1.InstanceState_INSTANCE_STATE_TERMINATED:
		m.tombstones[id] = struct{}{}
	case goblinv1.InstanceState_INSTANCE_STATE_ARCHIVED:
		m.tombstones[id] = struct{}{}
		delete(m.instances, id)
	}
	return nil
}

// SignalInstance mirrors the FSM's authorization: signalable state and
// sufficient rights, answering with the placement node.
func (m *MockStore) SignalInstance(ctx context.Context, req *goblinv1.SignalRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := ident.String(req.InstanceUuid)
	inst, ok := m.instances[id]
	if !ok {
		return "", fmt.Errorf("mock signal: unknown instance %s", id)
	}
	if inst.State != goblinv1.InstanceState_INSTANCE_STATE_RUNNING {
		return "", fmt.Errorf("mock signal: instance %s not signalable", id)
	}
	required, err := gapicrypto.RightForSignal(req.Signum)
	if err != nil {
		return "", err
	}
	if req.Rights&required != required {
		return "", fmt.Errorf("mock signal: insufficient rights")
	}
	return inst.NodeId, nil
}

func (m *MockStore) GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.instances[instanceID]
	if !ok {
		return nil, false, nil
	}
	return proto.Clone(inst).(*goblinv1.AgentInstance), true, nil
}

func (m *MockStore) ListInstances(ctx context.Context) ([]*goblinv1.AgentInstance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*goblinv1.AgentInstance, 0, len(m.instances))
	for _, inst := range m.instances {
		out = append(out, proto.Clone(inst).(*goblinv1.AgentInstance))
	}
	return out, nil
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

// MockRPCClient implements RPCClient for tests that need agent dispatch
// to succeed. Every Call reports success; the assertions are on the
// instance state the scheduler records afterwards, not on the wire
// payload.
type MockRPCClient struct{}

func (MockRPCClient) Call(method string, req, resp proto.Message) error { return nil }

func (MockRPCClient) Close() error { return nil }

// NewMockClientFactory returns a ClientFactory whose clients always
// succeed. Passing nil for a Scheduler's factory is NOT the same thing:
// startAgentOnNode rejects a nil factory outright, so every dispatch
// fails and the instance is terminated. Tests that mean to exercise the
// happy path must inject this.
func NewMockClientFactory() ClientFactory {
	return func(addr string) (RPCClient, error) { return MockRPCClient{}, nil }
}

// BlockingRPCClient parks inside Call until released, so a test can hold
// a dispatch in flight and observe whether the reconciler waits for it.
type BlockingRPCClient struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (c BlockingRPCClient) Call(method string, req, resp proto.Message) error {
	c.entered <- struct{}{}
	<-c.release
	return nil
}

func (c BlockingRPCClient) Close() error { return nil }

// NewBlockingClientFactory returns a factory whose clients report on
// entered when a dispatch begins and stay parked until release closes.
func NewBlockingClientFactory(entered chan<- struct{}, release <-chan struct{}) ClientFactory {
	return func(addr string) (RPCClient, error) {
		return BlockingRPCClient{entered: entered, release: release}, nil
	}
}
