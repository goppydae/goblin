---
title: "goblinctl operator keygen"
---

## goblinctl operator keygen

Generate an operator Ed25519 keypair

### Synopsis

Writes <out>.key (PEM PKCS#8 private) and <out>.pub (hex public).
Pass the .pub path to goblind --operator-key; keep the .key off the cluster.

Never retire your last known-good key until the new key has authorized
one real change. The last registered key cannot be removed - that is what
stops a compromised node emptying the registry and re-seeding it with its
own key - but the rule counts keys, it cannot tell whether anyone holds
the private half. A registry holding only unreachable keys can neither
authorize anything nor be re-seeded.

Recovery is wiping the data dir on EVERY replica and re-bootstrapping;
wiping one node only makes it catch up from the leader and inherit the
same dead registry. On a cluster that has already run, that wipe also
destroys the instance table and the append-only tombstone set, making
terminated instance UUIDs re-admittable, and leaves the agent processes
themselves running to be reaped by hand.

```
goblinctl operator keygen [flags]
```

### Options

```
  -h, --help         help for keygen
      --out string   Output path prefix; writes <prefix>.key and <prefix>.pub
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

* [goblinctl operator](./goblinctl_operator/)	 - Manage operator identities

