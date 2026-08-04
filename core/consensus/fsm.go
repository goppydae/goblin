// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"errors"
	"fmt"
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

	// operatorSeed and operatorChain are the registry's PROVENANCE: the
	// founding key set and every signed change that actually mutated the
	// registry, in apply order. They exist so a snapshot can carry how
	// the registry reached its state rather than only the state itself
	// (GOBLIN-DIV-047).
	//
	// They are derived state, not an independent source of truth: every
	// write to them happens beside the write to operatorKeys that the
	// same log entry caused, so replaying them reproduces operatorKeys
	// exactly. Restore relies on that equality and refuses when it does
	// not hold.
	operatorSeed  *goblinv1.OperatorKeySeed
	operatorChain []*goblinv1.OperatorKeyChange

	// trustedRoots is this NODE's configured operator keys, from
	// --operator-key. It is the one piece of per-node configuration the
	// FSM is allowed to hold, and it is deliberately never consulted by
	// Apply - doing so would make the same log entry succeed on one
	// replica and fail on another, which is divergence.
	//
	// Restore is a different path with different rules. It is not
	// replicated decision-making; it is this node deciding whether to
	// adopt a root of trust that arrived over the wire, and that is
	// exactly the decision that must be made against something local.
	// Nil means the node was configured with no operator keys, which
	// makes it unable to authenticate a registry at all - see Restore.
	trustedRoots map[string]*goblinv1.OperatorKey

	mu sync.RWMutex
}

// NewFSM creates a new FSM
// NewFSM builds the state machine. trustedRoots is this node's configured
// operator keys (--operator-key), which Restore uses to authenticate a
// snapshot's registry; pass nil for a node configured with none.
//
// It is a CONSTRUCTOR PARAMETER rather than a setter, and that is not a
// style preference. A setter reintroduces an ordering hazard with real
// consequences: raft can call Restore as soon as the FSM is handed over,
// so an anchor installed "shortly after" construction is an anchor that
// might not be there when the first snapshot lands - and the failure
// would be silent acceptance of an unverified registry, which is the
// defect this closes.
func NewFSM(trustedRoots []*goblinv1.OperatorKey) *FSM {
	roots := make(map[string]*goblinv1.OperatorKey, len(trustedRoots))
	for _, k := range trustedRoots {
		roots[k.GetKeyId()] = &goblinv1.OperatorKey{
			KeyId:     k.GetKeyId(),
			PublicKey: append([]byte(nil), k.GetPublicKey()...),
			Comment:   k.GetComment(),
		}
	}
	return &FSM{
		state:        make(map[string]map[string][]byte),
		versions:     make(map[string]map[string]uint64),
		instances:    make(map[string]*goblinv1.AgentInstance),
		tombstones:   make(map[string]struct{}),
		migrations:   make(map[string]*goblinv1.MigrationRecord),
		operatorKeys: make(map[string]*goblinv1.OperatorKey),
		trustedRoots: roots,
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

	case goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_CHANGE:
		return f.applyOperatorKeyChange(cmd.GetOperatorKeyChange())

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
