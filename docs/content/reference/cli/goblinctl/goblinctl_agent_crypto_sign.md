---
title: "goblinctl agent crypto sign"
---

## goblinctl agent crypto sign

Sign a file and produce a detached .sig

```
goblinctl agent crypto sign <file> --key <private.pem> [flags]
```

### Options

```
  -h, --help         help for sign
      --key string   Path to private key (PEM)
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

