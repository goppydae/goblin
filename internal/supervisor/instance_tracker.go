package supervisor

import "sync"

// instanceTracker records the lifecycle state of NodeRPC-managed agent
// instances on this node; the heartbeat publisher snapshots it every
// cadence. States mirror the scheduler's vocabulary: running, failed.
type instanceTracker struct {
	mu     sync.Mutex
	states map[string]string
}

func newInstanceTracker() *instanceTracker {
	return &instanceTracker{states: make(map[string]string)}
}

func (t *instanceTracker) Set(instanceID, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.states[instanceID] = state
}

// SetIfTracked updates state only for instances this node manages; the
// local status feed carries every agent, not just scheduled instances.
func (t *instanceTracker) SetIfTracked(instanceID, state string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.states[instanceID]; ok {
		t.states[instanceID] = state
	}
}

func (t *instanceTracker) Remove(instanceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.states, instanceID)
}

func (t *instanceTracker) Snapshot() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.states))
	for k, v := range t.states {
		out[k] = v
	}
	return out
}
