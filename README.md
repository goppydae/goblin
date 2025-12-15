# Goblin - Distributed Orchestrator for GAPI

**Goblin** extends [GAPI](https://github.com/goppydae/gapi) with distributed orchestration capabilities, enabling multi-node agent supervision and cluster-wide coordination.

## Overview

While GAPI manages agents on a single node, Goblin adds:
- **Cluster Membership** - Node discovery via Serf
- **Consensus** - Leader election via Raft
- **Distributed Events** - Cluster-wide pub/sub messaging
- **Multi-Node Coordination** - Reconcile desired state across nodes

## Architecture

```
┌─────────────────────────────────────────────────┐
│                  Goblin Cluster                 │
├─────────────────────────────────────────────────┤
│                                                 │
│  Node A (Leader)         Node B         Node C  │
│  ┌──────────────┐     ┌──────────┐   ┌────────┐│
│  │ GAPI Local   │     │ GAPI     │   │ GAPI   ││
│  │ Supervisor   │     │ Super    │   │ Super  ││
│  └──────┬───────┘     │ visor    │   │ visor  ││
│         │             └────┬─────┘   └───┬────┘│
│         │                  │             │     │
│  ┌──────▼──────────────────▼─────────────▼────┐│
│  │     Distributed Event Bus (Goblin)         ││
│  │  - Cluster-wide pub/sub                    ││
│  │  - Leader-aware routing                    ││
│  │  - Eventual consistency                    ││
│  └────────────────────────────────────────────┘│
└─────────────────────────────────────────────────┘
```

## Quick Start

```bash
# Build
go build ./cmd/goblinctl

# Run single node
./goblinctl start

# Run multi-node cluster
# See docs/usage.md
```

## Documentation

- **[Installation](docs/usage.md)**: Setup and multi-node guide.
- **[Architecture](docs/architecture.md)**: Deep dive into Serf, Raft, and Event Bus.

## Project Structure

```
goblin/
├── cmd/
│   └── goblinctl/        # CLI & Daemon entry point
├── core/
│   ├── cluster/          # Serf Membership
│   ├── consensus/        # Raft Consensus
│   └── eventbus/         # Distributed Event Bus
├── docs/                 # Documentation
└── AGENTS.md             # Agent development guide
```

## License

Mozilla Public License 2.0 (MPL-2.0)
