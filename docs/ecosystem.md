# GoPPydae Ecosystem Architecture

## Overview

The GoPPydae ecosystem scales agent supervision from a single node to a
distributed cluster, along one line: **mechanism versus policy**.

## Products

### GAPI - local supervision kernel

Single-node agent lifecycle management.

- **Core libraries** (`gapi/core/*`): agent manager, lifecycle
  controller, event bus, transport, signal delivery, checkpoint.
- **Daemon** (`gapid`): standalone supervisor, and PID 1 when asked.
- **CLI** (`gapictl`): local agent operations.

Used for standalone nodes, development, and edge deployments.

### Goblin - distributed supervisor

Multi-node coordination and global scheduling.

- **Distributed primitives**: Raft consensus, Serf membership, a
  distributed event bus.
- **Scheduler**: global placement with redundancy.
- **Migration coordinator**: live process migration between nodes.
- **Embedded GAPI kernel**: in process, not a separate daemon.
- **Daemon** (`goblind`), **CLI** (`goblinctl`).

Used for production clusters and multi-region deployments.

## Architecture layers

```
+--------------------------------------------------------------+
|                     Application layer                        |
|              (trading agents, data pipelines)                |
+--------------------------------------------------------------+
|  GAPI daemon (gapid)     |  Goblin daemon (goblind)          |
|  - local supervision     |  - global scheduling              |
|  - port 14242 (QUIC)     |  - cluster coordination           |
|                          |  - embeds the GAPI kernel         |
|                          |  - port 29000 (QUIC + ALPN)       |
+--------------------------------------------------------------+
|               GAPI core libraries (gapi/core/*)              |
|  - agentmgr  - lifecycle  - eventbus  - transport            |
|  - procsig   - checkpoint - crypto    - cgroups              |
+--------------------------------------------------------------+
|                        ADK (Python/Go)                       |
|                   Agent Development Kit                      |
+--------------------------------------------------------------+
```

## Key design decisions

### 1. The kernel is a library

`gapi/core/*` are public APIs. `goblind` imports them and runs the agent
manager in process, so local supervision costs no network hop and both
products manage agents identically. A Goblin deployment is one binary.

### 2. Unified transport (QUIC + ALPN)

One port carries every protocol, routed by TLS ALPN.

```
Goblin QUIC listener :29000
|-- ALPN "gapi-quic"     -> embedded kernel protocol
|-- ALPN "goblin-rpc"    -> cluster RPC (SchedulerRPC, NodeRPC)
|-- ALPN "serf-quic"     -> membership gossip
|-- ALPN "raft-quic"     -> Raft replication
`-- ALPN "goblin-ckpt"   -> CRIU checkpoint images
```

One firewall rule, one certificate, stream multiplexing, and an
append-only registry so a collision is a review failure rather than a
runtime discovery.

### 3. Event bus scoping

- **Local** (in-memory): `agent.*`, `metrics.*`
- **Cluster** (Serf gossip): `cluster.node.*`, `cache.invalidate`
- **Leader** (Raft log): `global.config`, `job.assign`

### 4. Identity outlives location

An instance UUID is minted at admission and never changes. Where the
instance runs is a separate, mutable locator carried in gossip. That
separation is what makes live migration expressible: the process moves,
the identity does not.

## Deployment patterns

### Pattern 1: standalone GAPI

```bash
gapid
```

```bash
gapictl agent status
```

```bash
gapictl lifecycle start my-agent
```

Development, edge nodes, simple deployments.

### Pattern 2: Goblin cluster

Local agent management is always on - `goblind` embeds the kernel, so
there is no flag to enable it.

```bash
goblind start --id node1 --listen-addr 0.0.0.0:29000 --advertise-addr 10.0.0.1
```

```bash
goblind start --id node2 --listen-addr 0.0.0.0:29000 --advertise-addr 10.0.0.2 --join 10.0.0.1:29000
```

```bash
goblinctl tui
```

Production HA clusters and global scheduling.

### Pattern 3: separate GAPI daemon

```bash
gapid --runtime-addr 127.0.0.1:14242
```

```bash
goblind start --id node1 --listen-addr 0.0.0.0:29000
```

`goblind` still embeds its own kernel; running `gapid` alongside is for
supervising a distinct, non-clustered set of agents on the same host.

## Global agent scheduling

Shipped. Specs are registered with the cluster and the scheduler places
replicas across nodes, restarting them elsewhere on node failure.

```bash
goblinctl cluster agent register ./spec.yaml
```

```bash
goblinctl cluster agent scale my-agent 3
```

```bash
goblinctl cluster agent instances
```

A running instance can also be moved between nodes with its memory
intact:

```bash
goblinctl cluster migrate-instance <instance-uuid> node-2
```

## Security model

### GAPI (local)

- **Boundary**: process isolation and cgroups v2.
- **Provenance**: agent binaries carry a BLAKE3 `.b3` digest and an
  Ed25519 `.sig`. Enforcement is `supervisor.productionMode` in
  `gapid`'s config file, or `GAPI_SUPERVISOR_PRODUCTIONMODE` in its
  environment. **`gapid` has no `--production` flag** - it accepts only
  `--runtime-addr`, `--log-level`, `--pid1` and `--no-early-mounts`.
  `--production` is `goblind`'s flag, and the two are not the same
  switch: gapi's gates agent signature verification and nothing else,
  where goblin's also refuses to start without TLS.
- **TLS**: optional for local clients, and
  `transport.insecureSkipVerify` defaults to **true**. Production mode
  does not change that; set it to `false` yourself.

### Goblin (distributed)

- **Transport**: TLS 1.3 between nodes. Without configured
  certificates the daemon generates an ephemeral self-signed one and
  does not verify peers; `--production` refuses to start that way.
- **Authorization**: every mutating verb is gated on a capability
  token - Ed25519-signed, short-TTL, scoped to a named subject, and
  audited through Raft. A token minted for one instance cannot act on
  another. Rights are partitioned: the kernel owns bits 0-7 (signal
  delivery), the orchestrator owns bits 8 and up (`agent.register`,
  `agent.scale`, `agent.delete`, `node.drain`, `job.submit`,
  `job.migrate`, `event.publish`).
- **Checkpoint transfer**: a peer pulling an instance's memory image
  must present a token whose subject matches that instance.

## Performance characteristics

### Embedded kernel (in process)

- Agent queries: direct function call.
- Event propagation: memory speed.

### Goblin cluster

- Gossip propagation: ~100ms (SWIM, eventually consistent).
- Raft commit: ~10ms (quorum).
- Leader election: ~2s (timeout-based).

## Component ownership

| Component | GAPI | Goblin |
| --------- | ---- | ------ |
| Agent lifecycle | yes (core + daemon) | yes (via `core/`) |
| Checkpoint/restore mechanism | yes (`core/checkpoint`) | yes (via `core/`) |
| Signal delivery (pidfd + epoch) | yes (`core/procsig`) | yes (via `core/`) |
| Event bus (local) | yes | yes (via `core/`) |
| Event bus (distributed) | no | yes |
| Cluster membership | no | yes (Serf) |
| Consensus | no | yes (Raft) |
| Global scheduling | no | yes |
| Live migration policy | no | yes (`core/migration`) |
| Capability tokens | verification | issuance + verification |
| Metrics | local | aggregated |

## References

- [Goblin architecture](architecture.md) - component breakdown
- [CLI reference](cli-reference.md) - every flag and command
- [Usage](usage.md) - running a cluster
- Each repo's `divergence.jsonl` - where the design and the code
  currently disagree, and why
