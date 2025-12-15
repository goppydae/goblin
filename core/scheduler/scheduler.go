package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"github.com/goppydae/goblin/core/cluster"
	"github.com/goppydae/goblin/core/store"
)

type Scheduler struct {
	store   *store.Store
	cluster *cluster.Membership
}

func NewScheduler(s *store.Store, c *cluster.Membership) *Scheduler {
	return &Scheduler{
		store:   s,
		cluster: c,
	}
}

// Schedule selects a node for the job based on strategy.
// It does NOT assign the job; it only selects the target.
func (s *Scheduler) Schedule(job *Job, strategy Strategy) (string, error) {
	members := s.cluster.Members()
	if len(members) == 0 {
		return "", fmt.Errorf("no nodes available in cluster")
	}

	var nodes []string
	for _, m := range members {
		if m.Status == 1 { // MemberStatusAlive is usually 1 in Serf
			nodes = append(nodes, m.Name)
		}
	}

	if len(nodes) == 0 {
		return "", fmt.Errorf("no alive nodes found")
	}

	switch strategy {
	case StrategyRandom:
		rand.Seed(time.Now().UnixNano())
		return nodes[rand.Intn(len(nodes))], nil
	case StrategyRoundRobin:
		// Simplified RR: just random for now as we don't store last index
		rand.Seed(time.Now().UnixNano())
		return nodes[rand.Intn(len(nodes))], nil
	default:
		return "", fmt.Errorf("unknown strategy: %s", strategy)
	}
}

// Assign persists the assignment to the KV store.
func (s *Scheduler) Assign(ctx context.Context, jobID, nodeID string) error {
	// 1. Store Job assignment: /jobs/assignments/<node_id>/<job_id> -> job_id
	// We might want to store the full Job spec somewhere too, e.g. /jobs/specs/<job_id>
	// For simplicity, we assume job spec is already stored or passed.
	// Actually, let's just store the assignment so the node knows.

	key := fmt.Sprintf("/jobs/assignments/%s/%s", nodeID, jobID)
	// Value could be just "assigned" or the Job ID again.
	if err := s.store.Set(ctx, "default", key, []byte(jobID)); err != nil {
		return fmt.Errorf("failed to assign job: %w", err)
	}
	return nil
}

// RegisterJob stores the job definition.
func (s *Scheduler) RegisterJob(ctx context.Context, job *Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("/jobs/specs/%s", job.ID)
	return s.store.Set(ctx, "default", key, data)
}
