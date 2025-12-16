package store

import (
	"context"
	"errors"
	"time"

	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/eventbus"
	pb "github.com/goppydae/goblin/internal/proto"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNotLeader = errors.New("not leader")
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
	cmd := &pb.LogEntry{
		Type:      pb.CommandType_SET,
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
	cmd := &pb.LogEntry{
		Type:      pb.CommandType_DELETE,
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

	// Publish change event
	s.bus.PublishLocal("kv", "store.change", map[string]interface{}{
		"op":        "delete",
		"namespace": namespace,
		"key":       key,
	}, []string{"kv"})

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
