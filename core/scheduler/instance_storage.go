package scheduler

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// SaveInstance stores the agent instance state.
func (s *Scheduler) SaveInstance(ctx context.Context, instance *goblinv1.AgentInstance) error {
	if instance.InstanceId == "" {
		return fmt.Errorf("instance ID is required")
	}

	data, err := protojson.Marshal(instance)
	if err != nil {
		return fmt.Errorf("failed to marshal agent instance: %w", err)
	}

	key := fmt.Sprintf("/agents/instances/%s", instance.InstanceId)
	if err := s.store.Set(ctx, "default", key, data); err != nil {
		return fmt.Errorf("failed to store agent instance: %w", err)
	}

	return nil
}

// GetInstance retrieves an agent instance by ID.
func (s *Scheduler) GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, error) {
	key := fmt.Sprintf("/agents/instances/%s", instanceID)
	data, found, err := s.store.Get(ctx, "default", key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("agent instance not found: %s", instanceID)
	}

	var instance goblinv1.AgentInstance
	if err := protojson.Unmarshal(data, &instance); err != nil {
		return nil, fmt.Errorf("failed to unmarshal agent instance: %w", err)
	}

	return &instance, nil
}

// ListInstances retrieves agent instances, optionally filtered by specID.
func (s *Scheduler) ListInstances(ctx context.Context, specID string) ([]*goblinv1.AgentInstance, error) {
	prefix := "/agents/instances/"
	items, err := s.store.Scan(ctx, "default", prefix)
	if err != nil {
		return nil, fmt.Errorf("failed to scan agent instances: %w", err)
	}

	var instances []*goblinv1.AgentInstance
	for _, data := range items {
		var instance goblinv1.AgentInstance
		if err := protojson.Unmarshal(data, &instance); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "failed to unmarshal instance", logattr.Err(err))
			continue
		}

		if specID != "" && instance.SpecId != specID {
			continue
		}

		instances = append(instances, &instance)
	}

	return instances, nil
}

// DeleteInstance removes an agent instance.
func (s *Scheduler) DeleteInstance(ctx context.Context, instanceID string) error {
	key := fmt.Sprintf("/agents/instances/%s", instanceID)
	if err := s.store.Delete(ctx, "default", key); err != nil {
		return fmt.Errorf("failed to delete agent instance: %w", err)
	}
	return nil
}
