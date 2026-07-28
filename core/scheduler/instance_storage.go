package scheduler

import (
	"context"
	"fmt"

	"github.com/goppydae/goblin/internal/ident"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// GetInstance retrieves a live agent instance by canonical UUID string.
func (s *Scheduler) GetInstance(ctx context.Context, instanceID string) (*goblinv1.AgentInstance, error) {
	inst, found, err := s.store.GetInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("agent instance not found: %s", instanceID)
	}
	return inst, nil
}

// ListInstances retrieves live agent instances, optionally filtered by
// the spec's canonical UUID string.
func (s *Scheduler) ListInstances(ctx context.Context, specID string) ([]*goblinv1.AgentInstance, error) {
	all, err := s.store.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agent instances: %w", err)
	}
	if specID == "" {
		return all, nil
	}
	var instances []*goblinv1.AgentInstance
	for _, inst := range all {
		if ident.String(inst.SpecUuid) == specID {
			instances = append(instances, inst)
		}
	}
	return instances, nil
}
