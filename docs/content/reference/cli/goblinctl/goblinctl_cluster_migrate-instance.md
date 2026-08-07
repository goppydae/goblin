---
title: "goblinctl cluster migrate-instance"
---

## goblinctl cluster migrate-instance

Live-migrate a running instance to another node (CRIU)

### Synopsis

Live-migrate a running instance to another node.

The instance is checkpointed on its current node, its memory image is
transferred, and it is restored on the destination under the same
instance UUID. The process keeps its state; only its location changes.

If any step fails the instance is restored on its source and the
migration is reported as rolled back. If the rollback also fails the
instance is running nowhere, and the error says so explicitly.

```
goblinctl cluster migrate-instance <instance-uuid> <to-node> [flags]
```

### Options

```
  -h, --help   help for migrate-instance
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

