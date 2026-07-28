package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNotLeader = errors.New("not leader")

	// ErrCASMismatch mirrors consensus.ErrCASMismatch at the store boundary.
	ErrCASMismatch = consensus.ErrCASMismatch
)

// Store provides a distributed key-value store interface
type Store struct {
	consensus *consensus.Consensus
	bus       eventbus.EventBus
	timeout   time.Duration
}

// NewStore creates a new Store backed by the consensus engine
func NewStore(c *consensus.Consensus, bus eventbus.EventBus) *Store {
	return &Store{
		consensus: c,
		bus:       bus,
		timeout:   5 * time.Second, // Default timeout
	}
}

// Set writes a key-value pair to the store
func (s *Store) Set(ctx context.Context, namespace, key string, value []byte) error {
	cmd := &goblinv1.LogEntry{
		Type:      goblinv1.CommandType_SET,
		Namespace: namespace,
		Key:       key,
		Value:     value,
	}

	data, err := proto.Marshal(cmd)
	if err != nil {
		return err
	}

	if err := s.consensus.Apply(data, s.timeout); err != nil {
		return err
	}

	// Publish change event
	// The original instruction's `Code Edit` snippet was malformed and contained references to
	// `req.Key`, `req.Value`, and `s.logger` which are not defined in this context.
	// Assuming the intent was to add error handling to the existing `PublishLocal` call,
	// and to keep the existing payload structure, the change is applied as follows.
	if err := s.bus.PublishLocal("kv", "store.change", map[string]interface{}{
		"op":        "set",
		"namespace": namespace,
		"key":       key,
		"value":     string(value), // Assuming UTF-8 for now for JSON simplicity
	}, []string{"kv"}); err != nil {
		// As s.logger is not defined, we'll return the error for now.
		// In a real scenario, this might be logged or handled differently.
		return err
	}

	return nil
}

// CompareAndSwap writes value only if the key's current version equals
// casVersion (0 = create-if-absent). A lost race returns ErrCASMismatch;
// callers must not treat it as a transport failure.
func (s *Store) CompareAndSwap(ctx context.Context, namespace, key string, value []byte, casVersion uint64) error {
	cmd := &goblinv1.LogEntry{
		Type:       goblinv1.CommandType_CAS,
		Namespace:  namespace,
		Key:        key,
		Value:      value,
		CasVersion: casVersion,
	}

	data, err := proto.Marshal(cmd)
	if err != nil {
		return err
	}

	resp, err := s.consensus.ApplyWithResponse(data, s.timeout)
	if err != nil {
		return err
	}
	if respErr, ok := resp.(error); ok {
		return respErr
	}

	if err := s.bus.PublishLocal("kv", "store.change", map[string]interface{}{
		"op":        "cas",
		"namespace": namespace,
		"key":       key,
		"value":     string(value),
	}, []string{"kv"}); err != nil {
		return err
	}
	return nil
}

// GetWithVersion retrieves a value and its CAS version with the same
// leader-only consistency rules as Get.
func (s *Store) GetWithVersion(ctx context.Context, namespace, key string) ([]byte, uint64, bool, error) {
	if s.consensus.IsLeader() {
		if err := s.consensus.VerifyLeader(); err != nil {
			return nil, 0, false, ErrNotLeader
		}
	} else {
		return nil, 0, false, ErrNotLeader
	}

	val, ver, found := s.consensus.GetStateWithVersion(namespace, key)
	return val, ver, found, nil
}

// Get retrieves a value from the store with linearizable consistency
func (s *Store) Get(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	// Linearizable read: Verify we are still leader (or have a leader lease)
	// If we are mostly reading from local FSM, we rely on Raft invariants.
	// However, simple local read (s.consensus.GetState) is strictly consistent ONLY if we verify leader.

	if s.consensus.IsLeader() {
		// If we think we are leader, verify we still have quorum
		if err := s.consensus.VerifyLeader(); err != nil {
			return nil, false, ErrNotLeader
		}
	} else {
		// If we are follower, we arguably return stale data or should forward to leader?
		// For now, strict: only Leader serves consistent reads.
		// Relaxed: Follower returns what it has (eventually consistent).
		// Goal says "Linearizable Reads".
		return nil, false, ErrNotLeader
	}

	val, found := s.consensus.GetState(namespace, key)
	return val, found, nil
}

// Scan retrieves all keys matching a prefix
func (s *Store) Scan(ctx context.Context, namespace, prefix string) (map[string][]byte, error) {
	// For now, strict: only Leader serves consistent reads.
	if s.consensus.IsLeader() {
		if err := s.consensus.VerifyLeader(); err != nil {
			return nil, ErrNotLeader
		}
	} else {
		return nil, ErrNotLeader
	}

	return s.consensus.Scan(namespace, prefix), nil
}

// Delete removes a key from the store
func (s *Store) Delete(ctx context.Context, namespace, key string) error {
	cmd := &goblinv1.LogEntry{
		Type:      goblinv1.CommandType_DELETE,
		Namespace: namespace,
		Key:       key,
	}

	data, err := proto.Marshal(cmd)
	if err != nil {
		return err
	}

	if err := s.consensus.Apply(data, s.timeout); err != nil {
		return err
	}

	// Publish change event. The delete is already committed through Raft;
	// a publish failure must not misreport it, so it is logged rather than
	// propagated.
	if err := s.bus.PublishLocal("kv", "store.change", map[string]interface{}{
		"op":        "delete",
		"namespace": namespace,
		"key":       key,
	}, []string{"kv"}); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "publish store.change failed", logattr.Namespace(namespace), logattr.Key(key), logattr.Err(err))
	}

	return nil
}

// Watch subscribes to changes for a specific key
func (s *Store) Watch(ctx context.Context, namespace, key string) <-chan eventbus.Event {
	ch := make(chan eventbus.Event)

	sub := s.bus.Subscribe("store.change", func(e eventbus.Event) {
		pl := e.Payload
		if pl["namespace"] != namespace {
			return
		}
		// If key is provided (not empty), filter by it
		if key != "" && pl["key"] != key {
			return
		}

		// Non-blocking send or context check
		select {
		case <-ctx.Done():
			return
		case ch <- e:
		}
	})

	// Close channel when context is done
	go func() {
		<-ctx.Done()
		sub.Unsubscribe()
		close(ch)
	}()

	return ch
}
