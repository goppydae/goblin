---
title: "goblinctl agent verify"
---

## goblinctl agent verify

Verify agent binary integrity and authenticity

### Synopsis

Verify agent binary using hash chain and optional signature.

Verification steps:
  1. Binary hash (compares against .b3 file)
  2. Signature (if .sig file exists and --pubkey provided)
  3. Source hash (if --check-source and source available)

Examples:
  goblinctl agent verify agents/my_service.go.service
  goblinctl agent verify agents/my_service.go.service --pubkey=key.pub
  goblinctl agent verify agents/my_service.go.service --check-source --source=src/agents/my_service.go.service

```
goblinctl agent verify <binary> [flags]
```

### Options

```
      --check-source    Verify source hash
  -h, --help            help for verify
      --pubkey string   Public key for signature verification
      --source string   Source directory path (for source verification)
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

