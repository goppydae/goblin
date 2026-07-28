package consensus

import (
	"errors"
	"fmt"
	"sort"

	"github.com/hashicorp/raft"
)

// Bootstrap faults are configuration faults, not transient conditions:
// a node that sees them must fail loudly rather than retry into a
// split cluster.
var (
	// ErrBootstrapExpectMismatch means peers disagree on how large the
	// seed set is. Each disagreeing subset would elect its own
	// bootstrapper and produce two clusters that gossip but never share
	// a log - the exact failure the seed model was introduced to avoid.
	ErrBootstrapExpectMismatch = errors.New("peers disagree on bootstrap-expect")

	// ErrDuplicateNodeID means two members advertise the same Raft
	// server ID. Raft would treat them as one server at two addresses.
	ErrDuplicateNodeID = errors.New("duplicate node id in bootstrap set")
)

// BootstrapPeer is one member's view as gossip reports it: its Raft
// server ID, its dialable control-plane address, and the
// bootstrap-expect size it was configured with (0 when it advertised
// none, which marks it a plain joiner rather than a seed).
type BootstrapPeer struct {
	NodeID string
	Addr   string
	Expect int
}

// BootstrapPlan is the decision every seed node reaches independently
// once it can see the whole expected set. Because it is a pure
// function of that set, all seeds compute the same plan and exactly
// one of them acts on it.
type BootstrapPlan struct {
	// Servers is the initial Raft configuration, ordered by node ID.
	Servers []raft.Server
	// Bootstrapper is the node ID that issues BootstrapCluster. The
	// other seeds do nothing and join through the resulting log.
	Bootstrapper string
}

// PlanBootstrap decides whether the seed set is complete and, if so,
// which member seeds the cluster. It reports ready=false while peers
// are still missing; callers poll until it turns true, the context
// expires, or it returns an error.
//
// expect < 2 means bootstrap-expect is not in play: the caller keeps
// the seed model (bootstrap alone when there is no join target).
func PlanBootstrap(expect int, peers []BootstrapPeer) (BootstrapPlan, bool, error) {
	if expect < 2 {
		return BootstrapPlan{}, false, nil
	}

	// Only members that advertised the same seed size participate.
	// A joiner that arrived early must not be baked into the initial
	// configuration, and a peer advertising a different size is a
	// misconfiguration we refuse to paper over.
	seeds := make([]BootstrapPeer, 0, len(peers))
	for _, p := range peers {
		switch p.Expect {
		case 0:
			continue // plain joiner
		case expect:
			seeds = append(seeds, p)
		default:
			return BootstrapPlan{}, false, fmt.Errorf(
				"%w: local expects %d, %s advertises %d",
				ErrBootstrapExpectMismatch, expect, p.NodeID, p.Expect)
		}
	}

	if len(seeds) < expect {
		return BootstrapPlan{}, false, nil
	}

	// Sorting by node ID is what makes the choice identical on every
	// seed regardless of the order gossip delivered members in.
	sort.Slice(seeds, func(i, j int) bool { return seeds[i].NodeID < seeds[j].NodeID })

	servers := make([]raft.Server, 0, len(seeds))
	for i, p := range seeds {
		if i > 0 && p.NodeID == seeds[i-1].NodeID {
			return BootstrapPlan{}, false, fmt.Errorf("%w: %s", ErrDuplicateNodeID, p.NodeID)
		}
		servers = append(servers, raft.Server{
			ID:       raft.ServerID(p.NodeID),
			Address:  raft.ServerAddress(p.Addr),
			Suffrage: raft.Voter,
		})
	}

	return BootstrapPlan{Servers: servers, Bootstrapper: seeds[0].NodeID}, true, nil
}
