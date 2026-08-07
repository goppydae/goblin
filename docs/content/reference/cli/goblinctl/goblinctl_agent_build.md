---
title: "goblinctl agent build"
---

## goblinctl agent build

Build Go agents

### Synopsis

Build Go agents from source and generate checksums.

Examples:
  goblinctl agent build src/agents/init.go.service
  goblinctl agent build src/agents/
  goblinctl agent build --watch src/agents/cluster_join.go.service
  goblinctl agent build --sign --key=agent-signing.key src/agents/init.go.service

```
goblinctl agent build [path] [flags]
```

### Options

```
      --cgo             Build the agent with cgo enabled (default: disabled, so no C compiler is needed)
  -h, --help            help for build
      --key string      Path to ED25519 signing key
  -o, --output string   Output directory for built agents (default "agents")
      --sign            Sign the built binary with ED25519
  -w, --watch           Watch for changes and rebuild
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

