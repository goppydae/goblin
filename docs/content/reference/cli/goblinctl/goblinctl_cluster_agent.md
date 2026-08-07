---
title: "goblinctl cluster agent"
---

## goblinctl cluster agent

Global agent specification management (specs, scheduling)

### Options

```
  -h, --help   help for agent
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

* [goblinctl cluster](./goblinctl_cluster/)	 - Cluster management operations
* [goblinctl cluster agent delete](./goblinctl_cluster_agent_delete/)	 - Delete a global agent specification
* [goblinctl cluster agent get](./goblinctl_cluster_agent_get/)	 - Get details of a global agent
* [goblinctl cluster agent instances](./goblinctl_cluster_agent_instances/)	 - List scheduled instances (all specs when no id is given)
* [goblinctl cluster agent list](./goblinctl_cluster_agent_list/)	 - List all global agent specifications
* [goblinctl cluster agent register](./goblinctl_cluster_agent_register/)	 - Register or update a global agent specification
* [goblinctl cluster agent scale](./goblinctl_cluster_agent_scale/)	 - Scale a global agent
* [goblinctl cluster agent signal](./goblinctl_cluster_agent_signal/)	 - Signal an instance (authorized and audited through Raft)

