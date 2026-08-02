package supervisor

import (
	"sort"
	"testing"

	gapiagentmgr "github.com/goppydae/gapi/core/agentmgr"
	gapilifecycle "github.com/goppydae/gapi/core/lifecycle"
)

// GOBLIN-DIV-050's gate.
//
// The entry's exit asks for a test in which one agent starts at boot and
// another, IDENTICAL BUT FOR ITS TARGET MEMBERSHIP, does not. That
// phrasing is doing real work: an assertion that a boot agent starts
// proves nothing on its own, because an unfiltered StartAll - the thing
// this mechanism exists to avoid - passes it. The discriminating half is
// the agent that must NOT start, and the two must differ in exactly one
// field or the test is about something else.

// fakeAgent is the smallest thing satisfying the kernel's Agent
// interface. Only the graph methods carry meaning here; the rest exist
// because the interface requires them.
type fakeAgent struct {
	id         string
	requires   []string
	wants      []string
	wantedBy   []string
	requiredBy []string
}

func (f *fakeAgent) ID() string                            { return f.id }
func (f *fakeAgent) Type() string                          { return "service" }
func (f *fakeAgent) Lang() string                          { return "go" }
func (f *fakeAgent) Dependencies() []string                { return f.requires }
func (f *fakeAgent) Controller() *gapilifecycle.Controller { return nil }
func (f *fakeAgent) Describe() map[string]string           { return map[string]string{"id": f.id} }
func (f *fakeAgent) Requires() []string                    { return f.requires }
func (f *fakeAgent) Wants() []string                       { return f.wants }
func (f *fakeAgent) WantedBy() []string                    { return f.wantedBy }
func (f *fakeAgent) RequiredBy() []string                  { return f.requiredBy }
func (f *fakeAgent) SetRunID(string)                       {}

func agentSet(agents ...*fakeAgent) map[string]gapiagentmgr.Agent {
	out := make(map[string]gapiagentmgr.Agent, len(agents))
	for _, a := range agents {
		out[a.id] = a
	}
	return out
}

func selectedIDs(m map[string]gapiagentmgr.Agent) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// THE CLOSING ASSERTION. Two agents differing in exactly one field:
// whether they name local.target in wanted_by.
func TestTargetMembershipDecidesWhoStartsAtBoot(t *testing.T) {
	bootLocal := &fakeAgent{id: "boot-local", wantedBy: []string{TargetLocal}}
	schedulable := &fakeAgent{id: "schedulable"} // identical but for this
	all := agentSet(bootLocal, schedulable)

	got := selectedIDs(agentsForTarget(all, TargetLocal))
	want := []string{"boot-local"}

	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("local.target selected %v, want %v.\n"+
			"If 'schedulable' is present, selection is not filtering by target "+
			"membership and the cluster scheduler will place instances of an "+
			"agent that is ALSO running as a node-local process on every node "+
			"(GOBLIN-DIV-050)", got, want)
	}
}

// A hard dependency of a boot-local agent is pulled in even though it
// names no target itself.
//
// Without the closure the kernel's sort would silently drop the edge - it
// ignores edges naming agents outside the map it is given - and the agent
// would start without the thing it declared it could not run without. The
// failure mode is a working boot with a missing dependency, which is
// worse than a refusal.
func TestRequiresIsPulledIntoTheTarget(t *testing.T) {
	dep := &fakeAgent{id: "dep"}
	boot := &fakeAgent{id: "boot", wantedBy: []string{TargetLocal}, requires: []string{"dep"}}
	all := agentSet(boot, dep)

	got := selectedIDs(agentsForTarget(all, TargetLocal))
	if len(got) != 2 {
		t.Fatalf("local.target selected %v, want both boot and its hard dep", got)
	}

	// And the order must put the dependency first.
	order, err := gapiagentmgr.TopologicalSort(agentsForTarget(all, TargetLocal))
	if err != nil {
		t.Fatalf("sort: %v", err)
	}
	if len(order) != 2 || order[0] != "dep" {
		t.Fatalf("start order %v, want dep before boot", order)
	}
}

// A soft dependency is NOT pulled in. This is the deliberate divergence
// from systemd, and it is what keeps a cluster-schedulable template from
// being recruited into a boot target by a single Wants edge.
func TestWantsDoesNotRecruitIntoTheTarget(t *testing.T) {
	optional := &fakeAgent{id: "optional"}
	boot := &fakeAgent{id: "boot", wantedBy: []string{TargetLocal}, wants: []string{"optional"}}
	all := agentSet(boot, optional)

	got := selectedIDs(agentsForTarget(all, TargetLocal))
	if len(got) != 1 || got[0] != "boot" {
		t.Fatalf("local.target selected %v, want only boot: a soft edge must order, not recruit", got)
	}
}

// Each target selects only its own members, so the four boot targets are
// genuinely separate synchronization points rather than one list.
func TestTargetsAreDistinctSynchronizationPoints(t *testing.T) {
	all := agentSet(
		&fakeAgent{id: "at-local", wantedBy: []string{TargetLocal}},
		&fakeAgent{id: "at-network", wantedBy: []string{TargetNetworkReady}},
		&fakeAgent{id: "at-cluster", wantedBy: []string{TargetCluster}},
		&fakeAgent{id: "at-distributed", wantedBy: []string{TargetDistributed}},
		&fakeAgent{id: "schedulable"},
	)
	for target, want := range map[string]string{
		TargetLocal:        "at-local",
		TargetNetworkReady: "at-network",
		TargetCluster:      "at-cluster",
		TargetDistributed:  "at-distributed",
	} {
		got := selectedIDs(agentsForTarget(all, target))
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s selected %v, want [%s]", target, got, want)
		}
	}
}

// An agent naming a target the boot path never reaches is selected by
// nothing. It is reported by warnUnreachableTargets rather than started,
// because a typo in a target name is otherwise indistinguishable from a
// deliberately cluster-schedulable agent.
func TestUnknownTargetSelectsNothingAndIsDeclared(t *testing.T) {
	all := agentSet(&fakeAgent{id: "typo", wantedBy: []string{"local-target"}}) // missing the dot

	for _, target := range bootTargets {
		if got := selectedIDs(agentsForTarget(all, target)); len(got) != 0 {
			t.Fatalf("%s selected %v from an agent naming a non-target", target, got)
		}
	}
	declared := declaredTargets(all)
	if len(declared) != 1 || declared[0] != "local-target" {
		t.Fatalf("declaredTargets = %v, want the unreachable name so it can be reported", declared)
	}
	if isBootTarget("local-target") {
		t.Fatal("a name that is not a boot target reported as one")
	}
}
