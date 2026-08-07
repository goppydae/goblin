---
title: "goblinctl cluster agent signal"
---

## goblinctl cluster agent signal

Signal an instance (authorized and audited through Raft)

```
goblinctl cluster agent signal <instance-uuid> <signum> [flags]
```

### Options

```
  -h, --help   help for signal
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

* [goblinctl cluster agent](./goblinctl_cluster_agent/)	 - Global agent specification management (specs, scheduling)

