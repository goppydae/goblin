# Goblin Architecture

Goblin is the **distributed supervisor** within the GoPPydae ecosystem. It provides cluster coordination, global scheduling, and multi-node resilience on top of GAPI's local supervision capabilities.

## Design Philosophy

**Goblin = GAPI + Clustering**

Goblin does not reinvent agent supervision. Instead, it:

1. Imports GAPI core libraries (`gapi/core/*`) for local agent management
1. Adds distributed primitives: consensus (Raft), membership (Serf), event bus
1. Provides global scheduling with redundancy and failover

## Relationship to GAPI

```
┌─────────────────────────────────┐
│      Goblin Node (goblind)      │
├─────────────────────────────────┤
│  Cluster Components             │
│  • Serf (membership/gossip)     │
│  • Raft (consensus/leader)      │
│  • Distributed Event Bus        │
│  • Global Scheduler             │
├─────────────────────────────────┤
│  GAPI Core (imported libs)      │
│  • agentmgr  • lifecycle        │  ← From gapi/core/*
├─────────────────────────────────┤
│  Local Agents (ADK)             │
└─────────────────────────────────┘
```

**Key Insight**: Each Goblin node uses GAPI's agent manager **in-process**. No separate GAPI daemon needed.

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

| Scope       | Method             | Transport   | Consistency  | Example                           |
| ----------- | ------------------ | ----------- | ------------ | --------------------------------- |
| **Local**   | `Publish()`        | Memory      | None (Local) | `agent.started`                   |
| **Cluster** | `PublishCluster()` | Serf Gossip | Eventual     | `node.joined`, `cache.invalidate` |
| **Leader**  | `PublishLeader()`  | Raft Log    | Strong       | `global.config`, `job.assign`     |

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

1. **Leader Election**:

   - Nodes bootstrap Raft.
   - Nodes vote.
   - Leader elected.
   - Leader starts applying FSM logs.

1. **Event Propagation**:

   - **Gossip**: A node calls `PublishCluster("mytopic", data)`. Serf broadcasts it. All nodes receive `UserEvent`, unpack it, and `Publish()` it to their local bus.
   - **Consensus**: A node calls `PublishLeader("cmd", data)`. It is forwarded to the Leader. Leader calls `Raft.Apply()`. Once committed, FSM on *all* nodes updates state.

## Diagram

```mermaid
graph TD
    subgraph Node A [Leader]
        GAPI_A[GAPI Core Libs]
        AgentMgr_A[Agent Manager]
        EB_A[Event Bus]
        Serf_A[Serf Agent]
        Raft_A[Raft Leader]
        FSM_A[FSM]
        Sched_A[Scheduler]
        
        GAPI_A --> AgentMgr_A
        AgentMgr_A --> EB_A
        EB_A -- "Cluster Event" --> Serf_A
        EB_A -- "Leader Event" --> Raft_A
        Raft_A --> FSM_A
        Serf_A --> EB_A
        Sched_A --> AgentMgr_A
    end

    subgraph Node B [Follower]
        GAPI_B[GAPI Core Libs]
        AgentMgr_B[Agent Manager]
        EB_B[Event Bus]
        Serf_B[Serf Agent]
        Raft_B[Raft Follower]
        FSM_B[FSM]
        
        GAPI_B --> AgentMgr_B
        AgentMgr_B --> EB_B
        Serf_A <.. Gossip ..> Serf_B
        Raft_A == "AppendEntries" ==> Raft_B
        Raft_B --> FSM_B
        Serf_B --> EB_B
    end
```

## Transport Architecture

**Single QUIC Port (29000) with ALPN Multiplexing:**

```
QUIC Listener :29000
├─ ALPN: "goblin-rpc"
│  └─ Goblin RPC (SchedulerRPC, cluster ops)
│
└─ ALPN: "gapi-quic"
   └─ GAPI Events (agent lifecycle, logs)
```

**Why Single Port:**

- Simplifies firewall rules
- Unified TLS certificate management
- Stream multiplexing over one connection
- ALPN provides protocol routing

**Local Agent Communication:**

- GAPI core runs **in-process** (no network needed)
- Agent → GAPI core: Direct function calls
- GAPI core → Scheduler: Event bus (memory)
