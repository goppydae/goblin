package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/consensus"
	"github.com/goppydae/goblin/core/metrics"
	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// ErrOperatorConfigStale means the cluster's registry does not contain
// this node's configured keys. It is deliberately NOT fatal.
//
// Once a registry is seeded, --operator-key is inert: it can only
// succeed as a no-op re-seed of an identical set or be refused, and the
// enforcement gate reads the replicated registry rather than config. So
// this condition is a configuration inconsistency, not an operational
// fault, and killing the node over it would turn untidiness into
// downtime - or, during a staggered rollout, into an availability
// incident.
//
// It is also not reliably distinguishable from a lost bootstrap race.
// hashicorp/raft restores only local state at construction and catches
// up from the leader afterward, so a brand new node joining a healthy
// cluster is indistinguishable, from inside this function, from a node
// that watched the registry fill with someone else's keys. An earlier
// attempt to tell them apart killed healthy nodes during scale-out.
// The disagreement is surfaced through the
// goblin_operator_key_config_drift gauge instead, which an operator can
// alert on rather than having it scroll past in a log.
//
// The gauge is set once, by the seeder below, and reports the situation
// as of that check; the seeder is one-shot and nothing re-evaluates it
// afterwards. Nothing can change the registry in band in piece 1, so
// the value cannot go stale there. Piece 2's change RPC must re-evaluate
// it when it commits, or the gauge starts lying.
var ErrOperatorConfigStale = errors.New("operator key config is stale: the cluster registry does not contain this node's configured keys")

// Operator key bootstrap (GOBLIN-DIV-015 piece 1).
//
// Configured keys are this node's claim about the cluster's root of
// trust. They do not become authoritative by being configured: they
// become authoritative by being committed to Raft, where every replica
// applies them identically. This file is the bridge between the two,
// and it is deliberately the only place config touches the registry.

// operatorRegistry is the consensus surface the seeder needs.
// *consensus.Consensus satisfies it. Declared narrowly here for the
// same reason voter.go declares its own: the seeder is testable against
// a real FSM without standing up Raft.
//
// It names the LOCAL read on purpose (GOBLIN-DIV-044). The seeder's two
// readers are both correct on stale state, and one of them is only ever
// correct on stale state - see driftedKeys. Widening this interface to
// the verified accessor would break the drift check outright.
type operatorRegistry interface {
	IsLeader() bool
	OperatorKeysLocal() ([]*goblinv1.OperatorKey, uint64)
	ApplyWithResponse(data []byte, timeout time.Duration) (interface{}, error)
}

// loadOperatorKeys reads every configured key file. One unreadable file
// fails the whole load: a node that silently came up with a subset of
// its configured root of trust is worse than one that refuses to start.
func loadOperatorKeys(paths []string) ([]*goblinv1.OperatorKey, error) {
	keys := make([]*goblinv1.OperatorKey, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		k, err := capability.LoadOperatorKey(p)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[k.GetKeyId()]; dup {
			return nil, fmt.Errorf("operator key %s is configured twice (key id %s)", p, k.GetKeyId())
		}
		seen[k.GetKeyId()] = struct{}{}
		keys = append(keys, k)
	}
	return keys, nil
}

// runOperatorKeySeeder commits the configured keys to the registry.
//
// It waits for leadership because only a leader can propose, and it
// polls rather than hooking LeaderCh so a node that is already the
// leader when it starts does not wait for an edge that never comes.
// With no configured keys it returns immediately: the cluster then has
// an empty registry and refuses every mutation, which is the documented
// fail-closed state, not an error to crash on.
//
// A refusal from the FSM is returned, not retried. ErrOperatorRegistrySeeded
// means this node's configured root of trust disagrees with the
// cluster's, and retrying a disagreement forever just hides it.
func runOperatorKeySeeder(ctx context.Context, reg operatorRegistry,
	keys []*goblinv1.OperatorKey, logger *slog.Logger, poll time.Duration) error {
	if len(keys) == 0 {
		// Say only what this node knows. The enforcement gate reads the
		// REPLICATED registry, not this node's config, so "no keys here"
		// does not imply "mutations are refused": in a cluster seeded by
		// another node, mutations through this one succeed. The old
		// wording asserted the refusal outright and named a remedy that
		// is inert once any node has seeded, which during an incident
		// points the operator at exactly the wrong conclusion.
		logger.LogAttrs(ctx, slog.LevelWarn,
			"this node contributed no operator key; whether mutations are refused depends on the cluster registry, which another node may have seeded",
			slog.String("remedy", "if no node was given --operator-key, the cluster refuses every mutating verb until one is"))
		return nil
	}
	if poll <= 0 {
		poll = time.Second
	}

	raw, err := proto.Marshal(&goblinv1.LogEntry{
		Type: goblinv1.CommandType_COMMAND_TYPE_OPERATOR_KEY_SEED,
		Payload: &goblinv1.LogEntry_OperatorKeySeed{
			OperatorKeySeed: &goblinv1.OperatorKeySeed{Keys: keys},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal operator key seed: %w", err)
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if reg.IsLeader() {
			resp, aerr := reg.ApplyWithResponse(raw, 10*time.Second)
			if aerr != nil {
				logger.LogAttrs(ctx, slog.LevelWarn,
					"operator key seed could not be committed; retrying", logattr.Err(aerr))
			} else if applyErr, ok := resp.(error); ok && applyErr != nil {
				// The FSM's verdict. A seed refusal is a configuration
				// fault, so it surfaces rather than spinning.
				metrics.OperatorKeyConfigDrift.Set(1)
				return fmt.Errorf("%w: operator key seed refused: %w", ErrOperatorConfigStale, applyErr)
			} else {
				// Local read, and safe: this branch runs on the node that
				// just committed the seed, so its own FSM has applied it by
				// construction. The values are a log line, not an
				// authorization decision.
				registered, serial := reg.OperatorKeysLocal()
				logger.LogAttrs(ctx, slog.LevelInfo, "operator key registry seeded",
					slog.Int("keys", len(registered)),
					slog.Uint64("serial", serial))
				metrics.OperatorKeyConfigDrift.Set(0)
				return nil
			}
		} else if drift := driftedKeys(reg, keys); len(drift) > 0 {
			// Not the leader, but the registry is populated and does not
			// contain what this node was configured with. Report it here
			// rather than waiting for a leadership change that may never
			// come: a node trusting keys the cluster does not is exactly
			// the state a config-drift check exists to catch.
			metrics.OperatorKeyConfigDrift.Set(1)
			return fmt.Errorf("%w: %w: configured key(s) %v are not in the cluster registry",
				ErrOperatorConfigStale, consensus.ErrOperatorRegistrySeeded, drift)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// driftedKeys lists configured key ids missing from a populated
// registry. An empty registry reports no drift: it has not been seeded
// yet, which is a race, not a disagreement.
//
// The read MUST be local (GOBLIN-DIV-044). Its only caller is the
// non-leader branch of the seeder, so the verified accessor would refuse
// every time and the drift check would never run - it exists precisely
// to report from a follower. The check is advisory and non-fatal in
// effect: it authorizes nothing, it sets a gauge and returns a
// non-fatal ErrOperatorConfigStale. A stale read here costs at worst a
// spurious drift report during catch-up, which is the failure direction
// this check is allowed to have.
func driftedKeys(reg operatorRegistry, configured []*goblinv1.OperatorKey) []string {
	registered, _ := reg.OperatorKeysLocal()
	if len(registered) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(registered))
	for _, k := range registered {
		have[k.GetKeyId()] = struct{}{}
	}
	var missing []string
	for _, k := range configured {
		if _, ok := have[k.GetKeyId()]; !ok {
			missing = append(missing, k.GetKeyId())
		}
	}
	return missing
}
