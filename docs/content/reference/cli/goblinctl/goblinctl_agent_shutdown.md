---
title: "goblinctl agent shutdown"
---

## goblinctl agent shutdown

Request a system shutdown from the daemon (poweroff by default)

```
goblinctl agent shutdown [--reboot|--halt] [flags]
```

### Options

```
      --halt     Halt instead of powering off
  -h, --help     help for shutdown
      --reboot   Reboot instead of powering off
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

* [goblinctl agent](./goblinctl_agent/)	 - Local agent management operations

