---
title: "goblinctl cluster"
---

## goblinctl cluster

Cluster management operations

### Options

```
  -h, --help   help for cluster
```

### Options inherited from parent commands

```
      --control-addr string   Address of the target daemon's control plane (empty: from config)
      --log-level string      Log level: debug, info, warn, error
      --tls-ca string         CA certificate used to verify the daemon
      --tls-insecure          Skip verification (INSECURE)
  -v, --version               version for goblinctl
```

### SEE ALSO

* [goblinctl](./goblinctl/)	 - Goblin distributed supervisor control
* [goblinctl cluster agent](./goblinctl_cluster_agent/)	 - Global agent specification management (specs, scheduling)
* [goblinctl cluster drain](./goblinctl_cluster_drain/)	 - Drain all jobs from a node
* [goblinctl cluster migrate](./goblinctl_cluster_migrate/)	 - Migrate a job to another node
* [goblinctl cluster migrate-instance](./goblinctl_cluster_migrate-instance/)	 - Live-migrate a running instance to another node (CRIU)
* [goblinctl cluster publish](./goblinctl_cluster_publish/)	 - Publish user event to cluster
* [goblinctl cluster run](./goblinctl_cluster_run/)	 - Submit a job to the cluster (YAML or JSON)
* [goblinctl cluster status](./goblinctl_cluster_status/)	 - Show cluster status

