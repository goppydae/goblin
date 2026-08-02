package consensus

// Snapshot and Restore: the FSM's serialized form.
//
// Split from fsm.go when GOBLIN-DIV-047 added registry provenance and the
// file passed the 500-line limit. The seam is not arbitrary - everything
// here is about state CROSSING A BOUNDARY, to disk or to a joining node,
// which is exactly where the trust question this file now answers lives.

import (
	"fmt"
	"io"

	"github.com/hashicorp/raft"
	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	goblinv1 "github.com/goppydae/goblin/proto"
)

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

	// The registry's provenance rides with it. Without this, the snapshot
	// carries a RESULT and a restoring node has no way to tell an honest
	// leader's result from a forged one (GOBLIN-DIV-047).
	var seedRaw []byte
	if f.operatorSeed != nil {
		raw, err := proto.Marshal(f.operatorSeed)
		if err != nil {
			return nil, fmt.Errorf("snapshot: marshal operator key seed: %w", err)
		}
		seedRaw = raw
	}
	chain := make([][]byte, 0, len(f.operatorChain))
	for i, chg := range f.operatorChain {
		raw, err := proto.Marshal(chg)
		if err != nil {
			return nil, fmt.Errorf("snapshot: marshal operator key change %d: %w", i, err)
		}
		chain = append(chain, raw)
	}

	return &fsmSnapshot{payload: &goblinv1.FSMSnapshot{
		Namespaces:             namespaces,
		Instances:              instances,
		Tombstones:             tombstones,
		Migrations:             migrations,
		OperatorKeys:           operatorKeys,
		OperatorRegistrySerial: f.operatorSerial,
		OperatorKeySeed:        seedRaw,
		OperatorKeyChain:       chain,
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
	//
	// Every record is re-validated. A snapshot arrives off the Apply
	// path, so none of the rules in fsm_operator_keys.go have run on
	// it; a malformed record refuses the whole Restore rather than
	// entering the registry, the same fail-loud choice as the
	// pre-schema-reset JSON refusal above.
	operatorKeys := make(map[string]*goblinv1.OperatorKey, len(payload.GetOperatorKeys()))
	for id, raw := range payload.GetOperatorKeys() {
		var k goblinv1.OperatorKey
		if err := proto.Unmarshal(raw, &k); err != nil {
			return fmt.Errorf("restore: unmarshal operator key %s: %w", id, err)
		}
		// A snapshot carries no authorization, so this is the only
		// place the id-is-derived invariant can be enforced off the
		// Apply path. It does not stop a well-formed hostile key -
		// nothing here can - but it does stop a stored record whose
		// id lies about its bytes, which every reader of this
		// registry assumes cannot exist.
		if err := capability.ValidateOperatorKey(&k); err != nil {
			return fmt.Errorf("restore: operator key %s: %w", id, err)
		}
		if k.GetKeyId() != id {
			return fmt.Errorf("restore: operator key filed under %q names %q", id, k.GetKeyId())
		}
		operatorKeys[id] = &k
	}

	// AUTHENTICATE THE REGISTRY BEFORE INSTALLING ANY OF IT
	// (GOBLIN-DIV-047). This runs before the lock and before a single
	// field is assigned, because a snapshot is atomic: a registry that
	// fails to verify must leave this node exactly as it was, not
	// half-restored with a hostile root of trust and honest key/value
	// state. The rest of the snapshot is not authenticated and does not
	// need to be - it is data the cluster already agreed on, whereas the
	// registry is what decides who may change that data.
	if err := f.verifyOperatorRegistry(operatorKeys, payload.GetOperatorRegistrySerial(),
		payload.GetOperatorKeySeed(), payload.GetOperatorKeyChain()); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	seed, chain, err := decodeOperatorProvenance(&payload)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
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
	// Carry the provenance forward. A node that restored from a snapshot
	// and then took its own snapshot must be able to hand the next node
	// the same evidence; dropping it here would make the chain survive
	// exactly one hop and then silently vanish.
	f.operatorSeed = seed
	f.operatorChain = chain
	return nil
}

// decodeOperatorProvenance re-decodes the provenance for storage. It runs
// after verifyOperatorRegistry has already parsed and checked the same
// bytes, which is a deliberate second pass: the verifier's job is to
// answer yes or no without leaving anything installed, so it keeps
// nothing, and this keeps the parsing that produces state separate from
// the parsing that produces a verdict.
func decodeOperatorProvenance(payload *goblinv1.FSMSnapshot) (*goblinv1.OperatorKeySeed, []*goblinv1.OperatorKeyChange, error) {
	raw := payload.GetOperatorKeySeed()
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var seed goblinv1.OperatorKeySeed
	if err := proto.Unmarshal(raw, &seed); err != nil {
		return nil, nil, fmt.Errorf("decode operator key seed: %w", err)
	}
	chain := make([]*goblinv1.OperatorKeyChange, 0, len(payload.GetOperatorKeyChain()))
	for i, craw := range payload.GetOperatorKeyChain() {
		var chg goblinv1.OperatorKeyChange
		if err := proto.Unmarshal(craw, &chg); err != nil {
			return nil, nil, fmt.Errorf("decode operator key change %d: %w", i, err)
		}
		chain = append(chain, &chg)
	}
	return &seed, chain, nil
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
