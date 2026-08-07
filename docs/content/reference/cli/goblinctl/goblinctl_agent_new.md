---
title: "goblinctl agent new"
---

## goblinctl agent new

Create a new agent from template

### Synopsis

Create a new agent from template with proper structure.

Examples:
  goblinctl agent new my_service
  goblinctl agent new --type=timer my_timer
  goblinctl agent new --lang=python --type=service my_py_service

```
goblinctl agent new [name] [flags]
```

### Options

```
  -h, --help            help for new
  -l, --lang string     Agent language (go, python) (default "go")
  -o, --output string   Output directory (default: agents/{lang}/foundational or agents/{lang}/services)
  -t, --type string     Agent type (service, socket, timer) (default "service")
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

