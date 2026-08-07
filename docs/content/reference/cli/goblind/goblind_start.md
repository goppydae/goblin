---
title: "goblind start"
---

## goblind start

Start the goblind daemon

```
goblind start [flags]
```

### Options

```
      --advertise-addr string             Advertise address (if different from bind)
      --agent-verify-key string           Path to the Ed25519 public key for agent signature verification (falls back to $GOBLIN_VERIFY_KEY)
      --bootstrap-expect int              Seed the cluster once this many nodes carrying the same value are visible; one of them is elected to bootstrap (0: seed model, the node with no --join bootstraps alone)
      --data string                       Data directory for Raft log (default "./data/raft")
      --encrypt string                    Base64 encoded 32-byte secret key for Serf encryption
  -h, --help                              help for start
      --join string                       Join existing cluster peer (host:port)
      --listen-addr string                Single control-plane bind address (QUIC; carries agent events, RPC, Serf, and Raft via ALPN) (default "127.0.0.1:31415")
      --network-gate-timeout duration     Block startup until the network agent reports running, failing after this bound (0: gate disabled)
      --no-early-mounts                   Skip the Phase 0 mount table (container environments)
      --operator-key stringArray          Path to a hex-encoded Ed25519 operator public key that bootstraps the cluster's operator registry (repeatable; with none, every mutating verb is refused). Keep the matching private key: the last registered key cannot be removed, so a registry left holding only keys nobody controls can neither authorize anything nor be re-seeded, and the only recovery is wiping the data dir on EVERY replica and re-bootstrapping. Produce keys with 'goblinctl operator keygen'. These are also this node's FOUNDING SEED: a snapshot's registry is authenticated by replaying its provenance from exactly this key set, so every node in a cluster must be given the same one, and a node joining after the registry has been rotated is still given the FOUNDING keys rather than the current ones. Keep them for the cluster's life (GOBLIN-DIV-047).
      --pid1                              Run as PID 1: kernel Phase 0 boot before the cluster stack; reversed teardown on shutdown
      --production                        Restrict agent discovery to binaries with verified signatures
      --raft-snapshot-interval duration   How often Raft checks whether a compaction snapshot is due (0: raft default, 120s)
      --raft-snapshot-threshold uint      Outstanding Raft log entries before a compaction snapshot (0: raft default, 8192)
      --raft-trailing-logs uint           Raft log entries retained after a snapshot for fast follower replay (0: raft default, 10240)
      --shutdown-grace duration           Per-phase shutdown grace (drain + agent stop) before forcing (default 10s)
      --watchdog-device string            Hardware watchdog device to keep alive (empty: disabled)
      --watchdog-interval duration        Watchdog keepalive kick interval (default 10s)
```

### Options inherited from parent commands

```
      --id string             Unique node identifier (default: hostname)
      --log-file string       Additional rotated file sink
      --log-format string     Log format: json or console
      --log-level string      Log level: debug, info, warn, error
      --log-loki-url string   Forward logs to a Loki endpoint
      --metrics-addr string   Prometheus listen address (empty: disabled)
      --tls-ca string         CA certificate for peer verification
      --tls-cert string       This node's certificate
      --tls-key string        This node's private key
  -v, --version               version for goblind
```

### SEE ALSO

* [goblind](./goblind/)	 - Goblin Distributed Supervisor Daemon

