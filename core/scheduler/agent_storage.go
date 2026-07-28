package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goppydae/goblin/internal/ident"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// RegisterAgent validates and stores the agent specification. Identity
// is the spec UUID (UUIDv7); the leader mints it here if the caller did
// not. Name is the operator-facing handle and must be unique among
// live specs.
func (s *Scheduler) RegisterAgent(ctx context.Context, spec *goblinv1.AgentSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("agent name is required")
	}
	if len(spec.SpecUuid) == 0 {
		if existing, err := s.GetAgent(ctx, spec.Name); err == nil {
			// Re-registering an existing name updates that spec instead
			// of minting a duplicate identity for the same handle.
			spec.SpecUuid = existing.SpecUuid
		} else {
			spec.SpecUuid = ident.NewV7()
		}
	}
	specID := ident.String(spec.SpecUuid)
	if specID == "" {
		return fmt.Errorf("spec UUID must be 16 bytes, got %d", len(spec.SpecUuid))
	}

	data, err := protojson.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal agent spec: %w", err)
	}

	key := fmt.Sprintf("/agents/specs/%s", specID)
	if err := s.store.Set(ctx, "default", key, data); err != nil {
		return fmt.Errorf("failed to store agent spec: %w", err)
	}

	return nil
}

// GetAgent retrieves an agent specification by canonical UUID string or
// by operator-facing name.
func (s *Scheduler) GetAgent(ctx context.Context, id string) (*goblinv1.AgentSpec, error) {
	if _, err := ident.Parse(id); err == nil {
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

	// Not a UUID: resolve as a name.
	specs, err := s.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		if spec.Name == id {
			return spec, nil
		}
	}
	return nil, fmt.Errorf("agent spec not found: %s", id)
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
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to unmarshal spec", logattr.Err(err))
			continue
		}
		specs = append(specs, &spec)
	}

	return specs, nil
}

// DeleteAgent removes an agent specification by UUID string or name.
func (s *Scheduler) DeleteAgent(ctx context.Context, id string) error {
	spec, err := s.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("/agents/specs/%s", ident.String(spec.SpecUuid))
	if err := s.store.Delete(ctx, "default", key); err != nil {
		return fmt.Errorf("failed to delete agent spec: %w", err)
	}
	return nil
}
