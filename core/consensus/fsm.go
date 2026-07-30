package consensus

import (
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
	// migrations holds in-flight migrations keyed by instance UUID.
	// It is FSM state, not a cache: it is what refuses a second
	// concurrent migration, so it must survive snapshot/restore or a
	// restarted leader would forget an arbitration it already made.
	migrations map[string]*goblinv1.MigrationRecord
	// operatorKeys is the registry of authorized operator identities
	// (GOBLIN-DIV-015 piece 1), keyed by key id. It is the root of trust
	// for every mutating verb: an empty registry authorizes nothing.
	//
	// It is FSM state and not per-node config on purpose. Config keys
	// differ between nodes; if Apply consulted them, the same log entry
	// would be accepted on one replica and refused on another, which is
	// divergence. Config reaches the registry only by being proposed as
	// an OPERATOR_KEY_SEED entry that every replica then applies
	// identically.
	operatorKeys map[string]*goblinv1.OperatorKey
	// operatorSerial is the registry's monotone version. It is the replay
	// guard for signed changes: a change names the serial it was signed
	// against and is dead once any other change lands.
	operatorSerial uint64
	mu             sync.RWMutex
}

// NewFSM creates a new FSM
func NewFSM() *FSM {
	return &FSM{
		state:        make(map[string]map[string][]byte),
		versions:     make(map[string]map[string]uint64),
		instances:    make(map[string]*goblinv1.AgentInstance),
		tombstones:   make(map[string]struct{}),
		migrations:   make(map[string]*goblinv1.MigrationRecord),
		operatorKeys: make(map[string]*goblinv1.OperatorKey),
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

	case goblinv1.CommandType_COMMAND_TYPE_MIGRATE_BEGIN:
		return f.applyMigrateBegin(cmd.GetMigrateBegin())

	case goblinv1.CommandType_COMMAND_TYPE_MIGRATE_COMMIT:
		return f.applyMigrateCommit(cmd.GetMigrateCommit())

	case goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED:
		return f.applyOperatorKeySeed(cmd.GetOperatorKeySeed())

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

// Snapshot returns a snapshot of the FSM state
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	namespaces := make(map[string]*goblinv1.FSMNamespaceState, len(f.state))
	for ns, kv := range f.state {
		values := make(map[string][]byte, len(kv))
		for k, v := range kv {
			val := make([]byte, len(v))
			copy(val, v)
			values[k] = val
		}
		versions := make(map[string]uint64, len(f.versions[ns]))
		for k, v := range f.versions[ns] {
			versions[k] = v
		}
		namespaces[ns] = &goblinv1.FSMNamespaceState{Values: values, Versions: versions}
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
	migrations := make(map[string][]byte, len(f.migrations))
	for id, rec := range f.migrations {
		raw, err := proto.Marshal(rec)
		if err != nil {
			return nil, fmt.Errorf("snapshot: marshal migration %s: %w", id, err)
		}
		migrations[id] = raw
	}

	operatorKeys := make(map[string][]byte, len(f.operatorKeys))
	for id, k := range f.operatorKeys {
		raw, err := proto.Marshal(k)
		if err != nil {
			return nil, fmt.Errorf("snapshot: marshal operator key %s: %w", id, err)
		}
		operatorKeys[id] = raw
	}

	return &fsmSnapshot{payload: &goblinv1.FSMSnapshot{
		Namespaces:             namespaces,
		Instances:              instances,
		Tombstones:             tombstones,
		Migrations:             migrations,
		OperatorKeys:           operatorKeys,
		OperatorRegistrySerial: f.operatorSerial,
	}}, nil
}

// Restore restores the FSM from a snapshot. Only the proto encoding is
// accepted (GOBLIN-DIV-040 schema reset): a snapshot written by the old
// JSON encoder is refused outright rather than dual-read, mirroring the
// CommandType-0 rejection above for the same reason - a compatibility
// path nothing forces anyone to remove never gets removed.
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

	if looksLikeJSON(raw) {
		return fmt.Errorf("snapshot appears to be a pre-schema-reset snapshot (JSON, not protobuf): " +
			"wipe the data dir and rejoin")
	}

	var payload goblinv1.FSMSnapshot
	if err := proto.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("restore: unmarshal snapshot: %w", err)
	}

	state := make(map[string]map[string][]byte, len(payload.GetNamespaces()))
	versions := make(map[string]map[string]uint64, len(payload.GetNamespaces()))
	for ns, nsState := range payload.GetNamespaces() {
		state[ns] = nsState.GetValues()
		versions[ns] = nsState.GetVersions()
	}

	instances := make(map[string]*goblinv1.AgentInstance, len(payload.GetInstances()))
	for id, raw := range payload.GetInstances() {
		var inst goblinv1.AgentInstance
		if err := proto.Unmarshal(raw, &inst); err != nil {
			return fmt.Errorf("restore: unmarshal instance %s: %w", id, err)
		}
		instances[id] = &inst
	}
	tombstones := make(map[string]struct{}, len(payload.GetTombstones()))
	for _, id := range payload.GetTombstones() {
		tombstones[id] = struct{}{}
	}

	// In-flight migrations. Restoring them is what keeps the concurrency
	// arbitration honest across a restart or a replica that caught up
	// from a snapshot rather than replaying the log.
	migrations := make(map[string]*goblinv1.MigrationRecord, len(payload.GetMigrations()))
	for id, raw := range payload.GetMigrations() {
		var rec goblinv1.MigrationRecord
		if err := proto.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("restore: unmarshal migration %s: %w", id, err)
		}
		migrations[id] = &rec
	}

	// The registry and its serial. Restoring the serial matters as much
	// as restoring the keys: it is the replay guard, and a leader that
	// came back from a snapshot with serial 0 would accept a signed
	// change it had already applied.
	operatorKeys := make(map[string]*goblinv1.OperatorKey, len(payload.GetOperatorKeys()))
	for id, raw := range payload.GetOperatorKeys() {
		var k goblinv1.OperatorKey
		if err := proto.Unmarshal(raw, &k); err != nil {
			return fmt.Errorf("restore: unmarshal operator key %s: %w", id, err)
		}
		operatorKeys[id] = &k
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.state = state
	f.versions = versions
	f.instances = instances
	f.tombstones = tombstones
	f.migrations = migrations
	f.operatorKeys = operatorKeys
	f.operatorSerial = payload.GetOperatorRegistrySerial()
	return nil
}

// looksLikeJSON reports whether raw's first non-whitespace byte opens a
// JSON object - the shape every pre-schema-reset snapshot has (the old
// encoder always wrote a top-level object). A proto-marshalled
// FSMSnapshot never starts with '{': the wire format's field tags are
// low-value bytes, and '{' (0x7b) as a field-1 varint tag would demand
// a wire type this schema does not use for field 1.
func looksLikeJSON(raw []byte) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b == '{'
		}
	}
	return false
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
	payload *goblinv1.FSMSnapshot
}

// Persist writes the snapshot to the sink
func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	raw, err := proto.Marshal(s.payload)
	if err != nil {
		if cerr := sink.Cancel(); cerr != nil {
			return fmt.Errorf("%w (also failed to cancel sink: %w)", err, cerr)
		}
		return err
	}
	if _, err := sink.Write(raw); err != nil {
		if cerr := sink.Cancel(); cerr != nil {
			return fmt.Errorf("%w (also failed to cancel sink: %w)", err, cerr)
		}
		return err
	}

	return sink.Close()
}

// Release is called when the snapshot is no longer needed
func (s *fsmSnapshot) Release() {}
