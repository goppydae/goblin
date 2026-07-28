package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// RegisterAgent validates and stores the agent specification.
func (s *Scheduler) RegisterAgent(ctx context.Context, spec *goblinv1.AgentSpec) error {
	if spec.Id == "" {
		return fmt.Errorf("agent ID is required")
	}

	// Use protojson for storage to be human-readable in KV/Consulate?
	// Or standard proto.Marshal? existing scheduler.go uses json.Marshal for Job.
	// But Job is a Go struct. Here we have a Proto generated struct.
	// protojson is better for JSON compatibility.
	data, err := protojson.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal agent spec: %w", err)
	}

	key := fmt.Sprintf("/agents/specs/%s", spec.Id)
	if err := s.store.Set(ctx, "default", key, data); err != nil {
		return fmt.Errorf("failed to store agent spec: %w", err)
	}

	return nil
}

// GetAgent retrieves an agent specification by ID.
func (s *Scheduler) GetAgent(ctx context.Context, id string) (*goblinv1.AgentSpec, error) {
	key := fmt.Sprintf("/agents/specs/%s", id)
	data, found, err := s.store.Get(ctx, "default", key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("agent spec not found: %s", id)
	}

	var spec goblinv1.AgentSpec
	if err := protojson.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal agent spec: %w", err)
	}

	return &spec, nil
}

// ListAgents retrieves all registered agent specifications.
func (s *Scheduler) ListAgents(ctx context.Context) ([]*goblinv1.AgentSpec, error) {
	prefix := "/agents/specs/"
	items, err := s.store.Scan(ctx, "default", prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to scan agent specs: %w", err)
	}

	var specs []*goblinv1.AgentSpec
	for _, data := range items {
		var spec goblinv1.AgentSpec
		if err := protojson.Unmarshal(data, &spec); err != nil {
			// Log warning but continue? Or fail?
			// For now, continue and skip malformed data (robustness)
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to unmarshal spec", logattr.Err(err))
			continue
		}
		specs = append(specs, &spec)
	}

	return specs, nil
}

// DeleteAgent removes an agent specification.
func (s *Scheduler) DeleteAgent(ctx context.Context, id string) error {
	key := fmt.Sprintf("/agents/specs/%s", id)
	if err := s.store.Delete(ctx, "default", key); err != nil {
		return fmt.Errorf("failed to delete agent spec: %w", err)
	}
	return nil
}
