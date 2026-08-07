---
title: "Architecture"
weight: 5
---

# Goblin Architecture

Goblin is the **distributed supervisor** of the GoPPydae ecosystem. It adds
cluster coordination, global scheduling and multi-node resilience on top of
GAPI's local supervision.

This is orientation, not the full design. It describes how Goblin is put
together well enough to decide whether to build on it. The complete target
architecture - the identity model, the FSM command set, the migration
protocol - is maintained in the goppydae-docs repository.

## Design philosophy

**Goblin = GAPI + clustering.** Goblin does not reinvent agent
supervision. It:

1. Embeds the GAPI kernel (`gapi/core/*`) for local agent management.
2. Adds distributed primitives: consensus (Raft), membership (Serf), a
   distributed event bus.
3. Provides global scheduling with redundancy, failover and live
   migration.

The split is mechanism versus policy. GAPI knows how to start, stop,
signal and checkpoint a process on one machine. Goblin decides which
machine, and what happens when that changes.

## Relationship to GAPI

```
+---------------------------------+
|      Goblin node (goblind)      |
+---------------------------------+
|  Cluster components             |
|  - Serf (membership/gossip)     |
|  - Raft (consensus/leader)      |
|  - Distributed event bus        |
|  - Global scheduler             |
|  - Migration coordinator        |
+---------------------------------+
|  GAPI kernel (embedded)         |
|  - agentmgr   - lifecycle       |
|  - procsig    - checkpoint      |
+---------------------------------+
|  Local agents (ADK)             |
+---------------------------------+
```

Each Goblin node runs GAPI's agent manager **in process**. There is no
separate GAPI daemon.

## Core components

### 1. Cluster membership (Serf)

Maintains the live node list and detects failures.

- **Technology**: Serf (SWIM-style gossip), carried over QUIC.
- **Discovery**: nodes find each other through `--join`.
- **Failure detection**: randomized probing.
- **User events**: the event bus's cluster scope. Fast, eventually
  consistent.

### 2. Consensus (Raft)

Provides strong consistency for state that cannot be eventually
consistent.

- **Technology**: `hashicorp/raft`, carried over QUIC.
- **Leader election**: one node leads; only the leader accepts writes.
- **Log replication**: commands are committed to a replicated FSM.

Instance identity lives here and is immutable. Instance *location* does
not: it lives in gossip, because it changes on every restart and
migration.

### 3. Distributed event bus

Wraps the embedded GAPI event bus and routes by intent:

| Scope | Method | Transport | Consistency | Example |
| ----- | ------ | --------- | ----------- | ------- |
| Local | `PublishLocal()` | memory | none | `agent.started` |
| Cluster | `PublishCluster()` | Serf gossip | eventual | `node.joined` |
| Leader | `PublishLeader()` | Raft log | strong | `job.assign` |

### 4. Transport: one port, five protocols

Everything shares a single QUIC listener, routed by TLS ALPN. There are
no separate Serf, Raft or API ports.

```
QUIC listener :31415
|-- ALPN "gapi-quic"     -> embedded GAPI kernel protocol
|-- ALPN "goblin-rpc"    -> goblinctl and scheduler RPC
|-- ALPN "serf-quic"     -> membership gossip
|-- ALPN "raft-quic"     -> Raft replication
`-- ALPN "goblin-ckpt"   -> CRIU checkpoint images (migration)
```

The registry is `core/transport/alpn.go`; the listener's view of it is
`registryALPNs` in `core/transport/listener.go`. Adding a row is a
registry change, and an ALPN with no adapter registered is refused with
`CodeALPNNotServing` rather than silently accepted.

`goblin-ckpt` is deliberately its own ALPN, and therefore its own QUIC
connection: a multi-gigabyte memory image sharing a connection-level
flow-control window with Raft heartbeats would degrade the control
plane exactly while a migration is in flight.

- **Security**: TLS 1.3. Without `--tls-cert`/`--tls-key` the daemon
  generates an ephemeral self-signed certificate and does not verify
  peers; `--production` refuses to start in that state.
- **Framing**: 1-byte stream type, 4-byte length, protobuf payload.

**Why one port**: simpler firewall rules, one certificate, stream
multiplexing, and ALPN doing the routing.

## Data flow

**Member join**: node starts, initializes Serf, joins existing members;
Serf emits `EventMemberJoin`; the bus publishes `cluster.node.joined`
locally on every node.

**Leader election**: nodes bootstrap Raft, vote, elect a leader; the
leader begins applying FSM commands.

**Event propagation**:

- *Gossip*: `PublishCluster(topic, data)` - Serf broadcasts, every node
  unpacks and republishes locally.
- *Consensus*: `PublishLeader(cmd, data)` - forwarded to the leader,
  applied through Raft, and the FSM on every node updates.

## Local agent communication

The GAPI kernel runs in process, so there is no network hop for local
supervision: agents talk to the kernel by direct call, and the kernel
reaches the scheduler over the in-memory event bus.
