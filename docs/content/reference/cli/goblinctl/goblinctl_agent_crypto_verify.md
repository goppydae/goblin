---
title: "goblinctl agent crypto verify"
---

## goblinctl agent crypto verify

Verify a file against its .sig

```
goblinctl agent crypto verify <file> --pub <public.hex> [flags]
```

### Options

```
  -h, --help         help for verify
      --pub string   Path to public key (hex)
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

* [goblinctl agent crypto](./goblinctl_agent_crypto/)	 - Cryptography utilities

