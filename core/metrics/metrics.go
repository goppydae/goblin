package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Registry is the global prometheus registry
	Registry = prometheus.NewRegistry()

	// RaftTerm gauge
	RaftTerm = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "goblin_raft_term",
		Help: "Current Raft term",
	})

	// RaftState gauge (0=Follower, 1=Candidate, 2=Leader, 3=Shutdown)
	RaftState = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "goblin_raft_state",
		Help: "Current Raft state (0=Follower, 1=Candidate, 2=Leader)",
	})

	// ClusterMembers gauge
	ClusterMembers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "goblin_cluster_members",
		Help: "Number of members in the cluster by status",
	}, []string{"status"})

	// JobCount gauge
	JobCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "goblin_jobs_total",
		Help: "Total number of jobs by status",
	}, []string{"status"})

	// OperatorKeyConfigDrift is 1 when some or all of this node's
	// configured --operator-key values are absent from the replicated
	// registry, and 0 otherwise. It exists because the condition is
	// otherwise only visible as a startup log line that scrolls away:
	// the flag is inert once a registry is seeded, so a node can run
	// indefinitely and correctly while its operator believes it
	// contributed a key it did not.
	//
	// Scope of the value: it is set exactly once, by the startup seeder
	// (internal/supervisor/operator_keys.go), and reflects the situation
	// as of that check. The seeder is a one-shot goroutine that returns
	// after setting it, so nothing re-evaluates it afterwards. That is
	// harmless in GOBLIN-DIV-015 piece 1, where no in-band way to change
	// the registry exists, and stops being harmless the moment piece 2
	// lands a change RPC: whatever applies a registry change must
	// re-evaluate this gauge, or it will report a stale verdict forever.
	OperatorKeyConfigDrift = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "goblin_operator_key_config_drift",
		Help: "1 when any of this node's configured operator keys were absent from the cluster registry at startup, 0 otherwise",
	})
)

func init() {
	Registry.MustRegister(RaftTerm)
	Registry.MustRegister(RaftState)
	Registry.MustRegister(ClusterMembers)
	Registry.MustRegister(JobCount)
	Registry.MustRegister(OperatorKeyConfigDrift)
}
