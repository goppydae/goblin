---
title: "goblinctl agent crypto"
---

## goblinctl agent crypto

Cryptography utilities

### Options

```
  -h, --help   help for crypto
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
* [goblinctl agent crypto age-keygen](./goblinctl_agent_crypto_age-keygen/)	 - Generate a new AGE identity
* [goblinctl agent crypto decrypt](./goblinctl_agent_crypto_decrypt/)	 - Decrypt data from stdin to stdout (AGE)
* [goblinctl agent crypto encrypt](./goblinctl_agent_crypto_encrypt/)	 - Encrypt data from stdin to stdout (AGE)
* [goblinctl agent crypto keygen](./goblinctl_agent_crypto_keygen/)	 - Generate a new Ed25519 keypair
* [goblinctl agent crypto sign](./goblinctl_agent_crypto_sign/)	 - Sign a file and produce a detached .sig
* [goblinctl agent crypto verify](./goblinctl_agent_crypto_verify/)	 - Verify a file against its .sig

