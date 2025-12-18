# GoPPydae Ecosystem Architecture

## Overview

The GoPPydae ecosystem provides a unified platform for agent-based supervision, scaling from single-node local management to globally distributed orchestration.

## Products

### GAPI - Local Supervision Kernel

**Purpose**: Single-node agent lifecycle management

**Components**:

- **Core Libraries** (`gapi/core/*`): Agent manager, lifecycle controller, event bus
- **Daemon** (`gapid`): Standalone supervisor for non-cluster nodes
- **CLI** (`gapictl`): Local agent operations

**Use Cases**:

- Standalone nodes (no clustering needed)
- Development/testing environments
- Edge deployments

______________________________________________________________________

### Goblin - Distributed Supervisor

**Purpose**: Multi-node cluster coordination and global scheduling

**Components**:

- **Distributed Primitives**: Raft consensus, Serf membership, distributed event bus
- **Scheduler**: Global job placement with redundancy
- **Local Agent Manager**: Uses GAPI core libraries in-process
- **Daemon** (`goblind`): Cluster node binary
- **CLI** (`goblinctl`): Cluster operations, unified TUI

**Use Cases**:

- Production clusters (high availability)
- Algorithmic trading (global redundancy)
- Multi-region deployments

______________________________________________________________________

## Architecture Layers

```
┌──────────────────────────────────────────────────────────────┐
│                     Application Layer                         │
│              (Trading Agents, Data Pipelines)                 │
├──────────────────────────────────────────────────────────────┤
│  GAPI Daemon (gapid)  │  Goblin Daemon (goblind)             │
│  • Local supervision  │  • Global scheduling                 │
│  • Port 4242         │  • Cluster coordination              │
│                       │  • Uses GAPI core (in-process)       │
│                       │  • Port 29000 (QUIC + ALPN)          │
├──────────────────────────────────────────────────────────────┤
│               GAPI Core Libraries (gapi/core/*)               │
│  • agentmgr  • lifecycle  • eventbus  • transport            │
├──────────────────────────────────────────────────────────────┤
│                     ADK (Python/Go)                           │
│               Agent Development Kit                           │
└──────────────────────────────────────────────────────────────┘
```

## Key Design Decisions

### 1. GAPI Core as Library

**Rationale**: Enable code reuse without embedding full daemons.

**Implementation**:

- `gapi/core/*` packages are public APIs
- Both `gapid` and `goblind` import from `core/`
- Semantic versioning for stability

**Benefits**:

- Single executable deployment (`goblind` only)
- Zero network overhead for local operations
- Consistent agent management across products

______________________________________________________________________

### 2. Unified Transport (QUIC + ALPN)

**Rationale**: Single port for all communication, protocol multiplexing via ALPN.

**Goblin Port 29000**:

```
QUIC Listener
├─ ALPN: "goblin-rpc"  → Cluster RPC (SchedulerRPC, Members, etc.)
└─ ALPN: "gapi-quic"   → Agent Events (lifecycle, logs, metrics)
```

**Benefits**:

- Simple firewall rules (one port)
- Unified TLS management
- Stream multiplexing

______________________________________________________________________

### 3. Event Bus Scoping

**Local Scope** (GAPI):

- `agent.*` - Agent lifecycle events
- `metrics.*` - Local metrics

**Cluster Scope** (Goblin via Serf):

- `cluster.node.*` - Membership changes
- `cache.invalidate` - Gossip-based coordination

**Leader Scope** (Goblin via Raft):

- `global.config` - Cluster-wide configuration
- `job.assign` - Scheduler decisions

______________________________________________________________________

## Deployment Patterns

### Pattern 1: Standalone GAPI

```bash
# Single node, no clustering
gapid daemon start
gapictl agent lifecycle --start my-agent
```

**Use Case**: Development, edge nodes, simple deployments

______________________________________________________________________

### Pattern 2: Goblin Cluster

```bash
# Node 1 (bootstrap)
goblind --id=node1 --enable-local-agents

# Node 2 (join)
goblind --id=node2 --join=node1:29010 --enable-local-agents

# Unified control
goblinctl tui  # Shows: cluster nodes + jobs + local agents
```

**Use Case**: Production HA clusters, global scheduling

______________________________________________________________________

### Pattern 3: Hybrid (Advanced)

```bash
# Separate GAPI daemon + Goblin cluster coordination
gapid daemon start --port=4242
goblind --port=29000  # No local agents
```

**Use Case**: Legacy migration, specialized deployments

______________________________________________________________________

## Migration Path

### From Standalone GAPI → Goblin Cluster

1. **Before**: Run `gapid` on each node
1. **After**: Run `goblind --enable-local-agents` on each node
1. **Result**: Agents managed locally, cluster provides global scheduling

**No Breaking Changes**: `gapictl` continues to work against local GAPI if using hybrid pattern.

______________________________________________________________________

## Global Agent Scheduling (Future)

**Vision**: Agents as first-class scheduled entities with redundancy.

```bash
# Submit agent job with replicas
goblinctl job submit-agent \
  --type=python-trader \
  --replicas=3 \
  --constraints region=us-east

# Scheduler places across 3 nodes
# Auto-restart on node failure via Raft watch
```

**Benefits for Algo Trading**:

- High availability (redundant instances)
- Geographic distribution (latency optimization)
- Automatic failover (Raft-based health monitoring)

______________________________________________________________________

## Security Model

### GAPI (Local)

- **Boundary**: Process isolation, cgroups
- **TLS**: Optional for local clients
- **Access**: Unix socket or localhost QUIC

### Goblin (Distributed)

- **Boundary**: Node-to-node mTLS (Raft/Serf)
- **TLS**: Required for cluster communication
- **Access**: Certificate-based authentication

______________________________________________________________________

## Performance Characteristics

### GAPI Core (In-Process)

- Agent queries: **< 1ms** (direct function call)
- Event propagation: **Memory-speed**
- Overhead: **None** (linked library)

### Goblin Cluster

- Gossip propagation: **~100ms** (SWIM eventual consistency)
- Raft commit: **~10ms** (2-phase quorum)
- Leader election: **~2s** (timeout-based)

______________________________________________________________________

## Component Ownership

| Component               | GAPI             | Goblin         |
| ----------------------- | ---------------- | -------------- |
| Agent Lifecycle         | ✅ Core + Daemon | ✅ Via `core/` |
| Event Bus (Local)       | ✅               | ✅ Via `core/` |
| Event Bus (Distributed) | ❌               | ✅             |
| Cluster Membership      | ❌               | ✅ (Serf)      |
| Consensus               | ❌               | ✅ (Raft)      |
| Global Scheduling       | ❌               | ✅             |
| Metrics Collection      | ✅ Local         | ✅ Aggregated  |

______________________________________________________________________

## References

- **GAPI AGENTS.md**: Single-node design principles
- **Goblin AGENTS.md**: Distributed system principles
- **Goblin docs/architecture.md**: Detailed component breakdown
- **Implementation Plan**: Phase-based migration strategy
