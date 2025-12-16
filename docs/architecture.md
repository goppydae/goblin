# Goblin Architecture

Goblin transforms GAPI into a distributed system. It layers clustering capabilities on top of the local GAPI supervisor.

## Core Components

### 1. Cluster Membership (Serf)

**Purpose**: Maintains a list of active nodes in the cluster and detects failures.

- **Technology**: [Serf](https://www.serf.io/) (Gossip Protocol / SWIM)
- **Functions**:
  - **Discovery**: Nodes find each other via a `join` list.
  - **Failure Detection**: Uses randomized pinging to detect dead nodes.
  - **User Events**: Broadcasts generic events (Event Bus "Cluster" scope) via gossip. It is eventually consistent but fast.

### 2. Consensus (Raft)

**Purpose**: Provides strong consistency for critical state (Leader Election, Global Configuration).

- **Technology**: [Raft](https://raft.github.io/) (via `hashicorp/raft`)
- **Functions**:
  - **Leader Election**: One node is elected leader. Only the leader handles "Leader" scoped events.
  - **Log Replication**: Commands are appended to a replicated log and committed to a state machine (FSM).
  - **FSM**: A Finite State Machine that applies committed commands. Currently implements a key-value store.

### 3. Distributed Event Bus

**Purpose**: Unifies local and distributed messaging.

The `DistributedEventBus` wraps the local GAPI `EventBus` and routes messages based on their intent:

| Scope | Method | Transport | Consistency | Example |
|---|---|---|---|---|
| **Local** | `Publish()` | Memory | None (Local) | `agent.started` |
| **Cluster** | `PublishCluster()` | Serf Gossip | Eventual | `node.joined`, `cache.invalidate` |
| **Leader** | `PublishLeader()` | Raft Log | Strong | `global.config`, `job.assign` |

### 4. RPC Transport (QUIC)

**Purpose**: Provides secure, multiplexed RPC communication between CLI and cluster nodes.

- **Technology**: QUIC (HTTP/3 transport) with Protobuf serialization
- **Port**: 9000 (default)
- **Security**: TLS 1.3 (auto-generated self-signed certs for development)
- **Protocol**: Custom framing - 1-byte stream type + 4-byte length + protobuf payload
- **ALPN**: `goblin-rpc`
- **Methods**: SchedulerRPC (`SubmitJob`, `DrainNode`, `MigrateJob`, `ListJobs`, `Members`)
- **Clients**: `goblinctl` CLI, future web dashboard

**Why QUIC**:
- Unified transport with GAPI
- Built-in TLS 1.3 (no separate TLS configuration)
- Stream multiplexing over single connection
- Better performance than HTTP/1.1
- Modern, extensible protocol

## Data Flow

1. **Member Join**:
   - Node starts, initializes Serf.
   - Joins existing members.
   - `Serf` emits `EventMemberJoin`.
   - Event Bus publishes `cluster.node.joined` locally on all nodes.

2. **Leader Election**:
   - Nodes bootstrap Raft.
   - Nodes vote.
   - Leader elected.
   - Leader starts applying FSM logs.

3. **Event Propagation**:
   - **Gossip**: A node calls `PublishCluster("mytopic", data)`. Serf broadcasts it. All nodes receive `UserEvent`, unpack it, and `Publish()` it to their local bus.
   - **Consensus**: A node calls `PublishLeader("cmd", data)`. It is forwarded to the Leader. Leader calls `Raft.Apply()`. Once committed, FSM on *all* nodes updates state.

## Diagram

```mermaid
graph TD
    subgraph Node A [Leader]
        GAPI_A[GAPI Supervisor]
        EB_A[Event Bus]
        Serf_A[Serf Agent]
        Raft_A[Raft Leader]
        FSM_A[FSM]
        
        GAPI_A --> EB_A
        EB_A -- "Cluster Event" --> Serf_A
        EB_A -- "Leader Event" --> Raft_A
        Raft_A --> FSM_A
        Serf_A --> EB_A
    end

    subgraph Node B [Follower]
        GAPI_B[GAPI Supervisor]
        EB_B[Event Bus]
        Serf_B[Serf Agent]
        Raft_B[Raft Follower]
        FSM_B[FSM]
        
        Serf_A <.. Gossip ..> Serf_B
        Raft_A == "AppendEntries" ==> Raft_B
        Raft_B --> FSM_B
        Serf_B --> EB_B
    end
```
