package consensus

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	pb "github.com/goppydae/goblin/internal/proto"
	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"
)

// FSM implements the Raft finite state machine
type FSM struct {
	// Namespace -> Key -> Value
	state map[string]map[string][]byte
	mu    sync.RWMutex
}

// NewFSM creates a new FSM
func NewFSM() *FSM {
	return &FSM{
		state: make(map[string]map[string][]byte),
	}
}

// Apply applies a Raft log entry to the FSM
func (f *FSM) Apply(log *raft.Log) interface{} {
	var cmd pb.LogEntry
	if err := proto.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal log entry: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Initialize namespace if not exists
	if _, ok := f.state[cmd.Namespace]; !ok {
		f.state[cmd.Namespace] = make(map[string][]byte)
	}

	switch cmd.Type {
	case pb.CommandType_SET:
		f.state[cmd.Namespace][cmd.Key] = cmd.Value
	case pb.CommandType_DELETE:
		delete(f.state[cmd.Namespace], cmd.Key)
		// Clean up empty namespace? Optional.
		if len(f.state[cmd.Namespace]) == 0 {
			delete(f.state, cmd.Namespace)
		}
	}

	return nil
}

// Snapshot returns a snapshot of the FSM state
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	// Clone state deeply
	snapshot := make(map[string]map[string][]byte)
	for ns, kv := range f.state {
		snapshot[ns] = make(map[string][]byte)
		for k, v := range kv {
			// Copy bytes
			val := make([]byte, len(v))
			copy(val, v)
			snapshot[ns][k] = val
		}
	}

	return &fsmSnapshot{state: snapshot}, nil
}

// Restore restores the FSM from a snapshot
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	var state map[string]map[string][]byte
	if err := json.NewDecoder(rc).Decode(&state); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.state = state
	return nil
}

// Get retrieves a value from the FSM state
func (f *FSM) Get(namespace, key string) ([]byte, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if kv, ok := f.state[namespace]; ok {
		val, found := kv[key]
		return val, found
	}
	return nil, false
}

// fsmSnapshot implements raft.FSMSnapshot
type fsmSnapshot struct {
	state map[string]map[string][]byte
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
