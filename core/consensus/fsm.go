package consensus

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/hashicorp/raft"
)

// FSM implements the Raft finite state machine
type FSM struct {
	state map[string]interface{}
	mu    sync.RWMutex
}

// NewFSM creates a new FSM
func NewFSM() *FSM {
	return &FSM{
		state: make(map[string]interface{}),
	}
}

// Apply applies a Raft log entry to the FSM
func (f *FSM) Apply(log *raft.Log) interface{} {
	var cmd map[string]interface{}
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Simple key-value store
	if key, ok := cmd["key"].(string); ok {
		if value, ok := cmd["value"]; ok {
			f.state[key] = value
		}
	}

	return nil
}

// Snapshot returns a snapshot of the FSM state
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Clone state
	snapshot := make(map[string]interface{})
	for k, v := range f.state {
		snapshot[k] = v
	}

	return &fsmSnapshot{state: snapshot}, nil
}

// Restore restores the FSM from a snapshot
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	var state map[string]interface{}
	if err := json.NewDecoder(rc).Decode(&state); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.state = state
	return nil
}

// Get retrieves a value from the FSM state
func (f *FSM) Get(key string) (interface{}, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	value, ok := f.state[key]
	return value, ok
}

// fsmSnapshot implements raft.FSMSnapshot
type fsmSnapshot struct {
	state map[string]interface{}
}

// Persist writes the snapshot to the sink
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	err := json.NewEncoder(sink).Encode(s.state)
	if err != nil {
		sink.Cancel()
		return err
	}

	return sink.Close()
}

// Release is called when the snapshot is no longer needed
func (s *fsmSnapshot) Release() {}
