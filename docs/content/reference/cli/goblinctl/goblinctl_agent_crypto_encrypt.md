---
title: "goblinctl agent crypto encrypt"
---

## goblinctl agent crypto encrypt

Encrypt data from stdin to stdout (AGE)

```
goblinctl agent crypto encrypt --recipient <pubkey> [flags]
```

### Options

```
  -h, --help                help for encrypt
  -r, --recipient strings   Recipient public key(s)
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

