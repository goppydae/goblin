package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/scheduler"
	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// memKVStore is a minimal in-memory scheduler.KVStore: enough to back
// RegisterAgent (Set/Get/Scan/Delete against the "default" namespace).
// The instance-lifecycle methods are never reached by RegisterAgent and
// fail loudly if that assumption ever breaks.
type memKVStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKVStore() *memKVStore { return &memKVStore{data: map[string][]byte{}} }

func (m *memKVStore) storeKey(namespace, key string) string { return namespace + "\x00" + key }

func (m *memKVStore) Set(_ context.Context, namespace, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.storeKey(namespace, key)] = append([]byte(nil), value...)
	return nil
}

func (m *memKVStore) Get(_ context.Context, namespace, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[m.storeKey(namespace, key)]
	return v, ok, nil
}

func (m *memKVStore) Scan(_ context.Context, namespace, prefix string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]byte{}
	full := m.storeKey(namespace, prefix)
	for k, v := range m.data {
		if strings.HasPrefix(k, full) {
			out[strings.TrimPrefix(k, namespace+"\x00")] = v
		}
	}
	return out, nil
}

func (m *memKVStore) Delete(_ context.Context, namespace, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.storeKey(namespace, key))
	return nil
}

func (m *memKVStore) Admit(context.Context, []byte, []byte, string) error {
	return fmt.Errorf("memKVStore: Admit not implemented")
}

func (m *memKVStore) TransitionInstance(context.Context, []byte, goblinv1.InstanceState, string) error {
	return fmt.Errorf("memKVStore: TransitionInstance not implemented")
}

func (m *memKVStore) SignalInstance(context.Context, *goblinv1.SignalRequest) (string, error) {
	return "", fmt.Errorf("memKVStore: SignalInstance not implemented")
}

func (m *memKVStore) GetInstance(context.Context, string) (*goblinv1.AgentInstance, bool, error) {
	return nil, false, fmt.Errorf("memKVStore: GetInstance not implemented")
}

func (m *memKVStore) ListInstances(context.Context) ([]*goblinv1.AgentInstance, error) {
	return nil, fmt.Errorf("memKVStore: ListInstances not implemented")
}

// registerTestRPC builds a SchedulerRPC with a real capability issuer
// (authorize must succeed to reach the field check) and a real
// scheduler backed by an in-memory store (so a successful registration
// has somewhere to land).
func registerTestRPC(t *testing.T) *SchedulerRPC {
	t.Helper()
	issuer, err := capability.NewIssuer("node-1")
	if err != nil {
		t.Fatalf("new issuer: %v", err)
	}
	return &SchedulerRPC{
		issuer:      issuer,
		revocations: capability.NewRevocations(),
		members:     membersFor(issuer),
		scheduler:   scheduler.NewScheduler(newMemKVStore(), nil, nil, nil, nil),
	}
}

// TestRegisterGlobalAgent_RejectsCallerSuppliedSpecUUID pins the
// server-owned-field rule (goblin-typed-rpc-design.md, "Server-owned
// fields must be rejected, not overwritten"): spec_uuid is minted by
// the leader at registration, so a caller that sets it gets
// ErrInvalidRequest naming the field rather than a silently accepted
// identity.
//
// Hypothesis: RegisterGlobalAgent rejects any request whose
// spec.spec_uuid is non-empty, and proceeds when it is empty. Disproof:
// either case behaving the other way - a set spec_uuid going through,
// or an unset one being refused.
func TestRegisterGlobalAgent_RejectsCallerSuppliedSpecUUID(t *testing.T) {
	cases := []struct {
		name      string
		spec      *goblinv1.AgentSpec
		wantErr   bool
		wantField string
	}{
		{
			name:      "spec_uuid set is rejected",
			spec:      &goblinv1.AgentSpec{Name: "web", Type: "sleeper", Replicas: 1, SpecUuid: ident.NewV7()},
			wantErr:   true,
			wantField: "spec_uuid",
		},
		{
			name:    "spec_uuid unset proceeds",
			spec:    &goblinv1.AgentSpec{Name: "web", Type: "sleeper", Replicas: 1, SpecUuid: nil},
			wantErr: false,
		},
		{
			name:      "spec is required (nil spec rejected)",
			spec:      nil,
			wantErr:   true,
			wantField: "spec",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := registerTestRPC(t)
			req := &goblinv1.RegisterGlobalAgentRequest{
				Spec: tc.spec,
			}
			var resp goblinv1.RegisterGlobalAgentResponse
			err := s.RegisterGlobalAgent(req, &resp)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("RegisterGlobalAgent succeeded; want error naming %q", tc.wantField)
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Errorf("err = %v, want errors.Is(err, ErrInvalidRequest)", err)
				}
				if !strings.Contains(err.Error(), tc.wantField) {
					t.Errorf("err = %v, want it to name %q", err, tc.wantField)
				}
				return
			}

			if err != nil {
				t.Fatalf("RegisterGlobalAgent failed: %v", err)
			}
			if len(resp.GetSpecUuid()) == 0 {
				t.Error("response spec_uuid is empty; registration should have minted one")
			}
			if resp.GetName() != "web" {
				t.Errorf("response name = %q, want %q", resp.GetName(), "web")
			}
			// The minted identity is deterministic (ident.SpecUUID derives
			// it from the name), so it can be asserted exactly.
			if got, want := ident.String(resp.GetSpecUuid()), ident.String(ident.SpecUUID("web")); got != want {
				t.Errorf("minted spec_uuid = %s, want %s", got, want)
			}
		})
	}
}
