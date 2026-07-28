package consensus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"
)

// ErrCASMismatch is returned (as the Apply response) when a CAS command's
// expected version does not match the key's current version. Errors are
// data: callers distinguish "CAS lost the race" from transport failures.
var ErrCASMismatch = errors.New("cas: version mismatch")

// FSM implements the Raft finite state machine
type FSM struct {
	// Namespace -> Key -> Value
	state map[string]map[string][]byte
	// Namespace -> Key -> version. Versions start at 1 on first write and
	// increment on every applied SET or CAS. DELETE clears the version, so
	// a re-created key restarts at 1 (versions are per-incarnation, not
	// ABA-proof across delete/recreate).
	versions map[string]map[string]uint64
	// instances is the typed instance table (canonical UUID string ->
	// record); tombstones holds every UUID ever terminated, append-only
	// forever (operator decision 2026-07-28). Both live in the snapshot.
	instances  map[string]*goblinv1.AgentInstance
	tombstones map[string]struct{}
	mu         sync.RWMutex
}

// NewFSM creates a new FSM
func NewFSM() *FSM {
	return &FSM{
		state:      make(map[string]map[string][]byte),
		versions:   make(map[string]map[string]uint64),
		instances:  make(map[string]*goblinv1.AgentInstance),
		tombstones: make(map[string]struct{}),
	}
}

// Apply applies a Raft log entry to the FSM. The returned value is the
// command's response (retrieved via raft.ApplyFuture.Response): nil on
// success, ErrCASMismatch on a failed CAS, an error for undecodable or
// unknown commands. A silent no-op on a distributed write is a data
// consistency hazard, so every path answers.
func (f *FSM) Apply(log *raft.Log) interface{} {
	var cmd goblinv1.LogEntry
	if err := proto.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal log entry: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch cmd.Type {
	case goblinv1.CommandType_COMMAND_TYPE_UNSPECIFIED:
		// Zero means either a truly unset type or a LogEntry written
		// before the 2026-07 schema reset (when SET was 0). Reject it
		// loudly: applying it as anything would silently corrupt state.
		return fmt.Errorf("log entry with unspecified command type (namespace %s, key %s): "+
			"possibly pre-schema-reset raft data - wipe the data dir and rejoin", cmd.Namespace, cmd.Key)

	case goblinv1.CommandType_COMMAND_TYPE_SET:
		f.write(cmd.Namespace, cmd.Key, cmd.Value)
		return nil

	case goblinv1.CommandType_COMMAND_TYPE_DELETE:
		if kv, ok := f.state[cmd.Namespace]; ok {
			delete(kv, cmd.Key)
			if len(kv) == 0 {
				delete(f.state, cmd.Namespace)
			}
		}
		if vs, ok := f.versions[cmd.Namespace]; ok {
			delete(vs, cmd.Key)
			if len(vs) == 0 {
				delete(f.versions, cmd.Namespace)
			}
		}
		return nil

	case goblinv1.CommandType_COMMAND_TYPE_CAS:
		current := f.versions[cmd.Namespace][cmd.Key] // 0 when absent
		if current != cmd.CasVersion {
			return fmt.Errorf("%w: key %s/%s is at version %d, expected %d",
				ErrCASMismatch, cmd.Namespace, cmd.Key, current, cmd.CasVersion)
		}
		f.write(cmd.Namespace, cmd.Key, cmd.Value)
		return nil

	case goblinv1.CommandType_COMMAND_TYPE_ADMIT:
		return f.applyAdmit(cmd.GetAdmit())

	case goblinv1.CommandType_COMMAND_TYPE_TRANSITION:
		return f.applyTransition(cmd.GetTransition())

	case goblinv1.CommandType_COMMAND_TYPE_SIGNAL:
		return f.applySignal(cmd.GetSignal())

	default:
		return fmt.Errorf("unknown command type %v (namespace %s, key %s)",
			cmd.Type, cmd.Namespace, cmd.Key)
	}
}

// write stores a value and bumps its version. Callers hold f.mu.
func (f *FSM) write(namespace, key string, value []byte) {
	if _, ok := f.state[namespace]; !ok {
		f.state[namespace] = make(map[string][]byte)
	}
	if _, ok := f.versions[namespace]; !ok {
		f.versions[namespace] = make(map[string]uint64)
	}
	f.state[namespace][key] = value
	f.versions[namespace][key]++
}

// snapshotPayload is the versioned snapshot encoding. SchemaVersion
// distinguishes it from legacy snapshots, which were a bare JSON state
// map. Version 2 adds the instance table and tombstone set; instance
// records are proto-marshaled (JSON base64 in the envelope).
type snapshotPayload struct {
	SchemaVersion int                          `json:"schema_version"`
	State         map[string]map[string][]byte `json:"state"`
	Versions      map[string]map[string]uint64 `json:"versions"`
	Instances     map[string][]byte            `json:"instances,omitempty"`
	Tombstones    []string                     `json:"tombstones,omitempty"`
}

// Snapshot returns a snapshot of the FSM state
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	state := make(map[string]map[string][]byte, len(f.state))
	for ns, kv := range f.state {
		state[ns] = make(map[string][]byte, len(kv))
		for k, v := range kv {
			val := make([]byte, len(v))
			copy(val, v)
			state[ns][k] = val
		}
	}
	versions := make(map[string]map[string]uint64, len(f.versions))
	for ns, vs := range f.versions {
		versions[ns] = make(map[string]uint64, len(vs))
		for k, v := range vs {
			versions[ns][k] = v
		}
	}
	instances := make(map[string][]byte, len(f.instances))
	for id, inst := range f.instances {
		raw, err := proto.Marshal(inst)
		if err != nil {
			return nil, fmt.Errorf("snapshot: marshal instance %s: %w", id, err)
		}
		instances[id] = raw
	}
	tombstones := make([]string, 0, len(f.tombstones))
	for id := range f.tombstones {
		tombstones = append(tombstones, id)
	}

	return &fsmSnapshot{payload: snapshotPayload{
		SchemaVersion: 2,
		State:         state,
		Versions:      versions,
		Instances:     instances,
		Tombstones:    tombstones,
	}}, nil
}

// Restore restores the FSM from a snapshot. Both the versioned encoding and
// the legacy bare-state-map encoding are accepted; legacy keys restore at
// version 1 (they exist, so CAS create-if-absent semantics must not fire).
func (f *FSM) Restore(rc io.ReadCloser) (err error) {
	defer func() {
		if cerr := rc.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	raw, err := io.ReadAll(rc)
	if err != nil {
		return err
	}

	var payload snapshotPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.SchemaVersion == 0 {
		// Legacy snapshot: bare state map.
		var state map[string]map[string][]byte
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		payload = snapshotPayload{State: state, Versions: make(map[string]map[string]uint64)}
		for ns, kv := range state {
			payload.Versions[ns] = make(map[string]uint64, len(kv))
			for k := range kv {
				payload.Versions[ns][k] = 1
			}
		}
	}
	if payload.State == nil {
		payload.State = make(map[string]map[string][]byte)
	}
	if payload.Versions == nil {
		payload.Versions = make(map[string]map[string]uint64)
	}

	// Instance table (schema v2; absent in v1 and legacy snapshots).
	instances := make(map[string]*goblinv1.AgentInstance, len(payload.Instances))
	for id, raw := range payload.Instances {
		var inst goblinv1.AgentInstance
		if err := proto.Unmarshal(raw, &inst); err != nil {
			return fmt.Errorf("restore: unmarshal instance %s: %w", id, err)
		}
		instances[id] = &inst
	}
	tombstones := make(map[string]struct{}, len(payload.Tombstones))
	for _, id := range payload.Tombstones {
		tombstones[id] = struct{}{}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.state = payload.State
	f.versions = payload.Versions
	f.instances = instances
	f.tombstones = tombstones
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

// GetWithVersion retrieves a value and its CAS version. A missing key
// reports version 0 (the value CAS create-if-absent expects).
func (f *FSM) GetWithVersion(namespace, key string) ([]byte, uint64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	kv, ok := f.state[namespace]
	if !ok {
		return nil, 0, false
	}
	val, found := kv[key]
	if !found {
		return nil, 0, false
	}
	return val, f.versions[namespace][key], true
}

// Scan returns all key-value pairs in a namespace that match the prefix
func (f *FSM) Scan(namespace, prefix string) map[string][]byte {
	f.mu.RLock()
	defer f.mu.RUnlock()

	result := make(map[string][]byte)
	if kv, ok := f.state[namespace]; ok {
		for k, v := range kv {
			if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
				val := make([]byte, len(v))
				copy(val, v)
				result[k] = val
			}
		}
	}
	return result
}

// fsmSnapshot implements raft.FSMSnapshot
type fsmSnapshot struct {
	payload snapshotPayload
}

// Persist writes the snapshot to the sink
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.payload); err != nil {
		if cerr := sink.Cancel(); cerr != nil {
			return fmt.Errorf("%w (also failed to cancel sink: %w)", err, cerr)
		}
		return err
	}

	return sink.Close()
}

// Release is called when the snapshot is no longer needed
func (s *fsmSnapshot) Release() {}
