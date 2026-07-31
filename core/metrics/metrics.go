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

	// OperatorKeyConfigDrift is 1 while this node's configured
	// --operator-key values are absent from the replicated registry, and
	// 0 otherwise. It exists because the condition is otherwise only
	// visible as a startup log line that scrolls away: the flag is inert
	// once a registry is seeded, so a node can run indefinitely and
	// correctly while its operator believes it contributed a key it did
	// not. A gauge stays raised for as long as the disagreement does.
	OperatorKeyConfigDrift = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "goblin_operator_key_config_drift",
		Help: "1 when this node's configured operator keys are absent from the cluster registry, 0 otherwise",
	})
)

func init() {
	Registry.MustRegister(RaftTerm)
	Registry.MustRegister(RaftState)
	Registry.MustRegister(ClusterMembers)
	Registry.MustRegister(JobCount)
	Registry.MustRegister(OperatorKeyConfigDrift)
}
