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
)

func init() {
	Registry.MustRegister(RaftTerm)
	Registry.MustRegister(RaftState)
	Registry.MustRegister(ClusterMembers)
	Registry.MustRegister(JobCount)
}
