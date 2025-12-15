# Goblin Project Agenda

---

## 🎯 Mission Statement

**Goblin (Orchestrator)**: Distributed coordination using GAPI as a library.
Leverages the robust local agent lifecycle management of GAPI to build a scalable, multi-node control plane.

### Core Objectives
1. **Distributed Coordination**: Masterless or leader-based orchestration (Raft/Serf).
2. **Zero Boilerplate**: Inherits GAPI's self-describing agent philosophy.
3. **Production Ready**: Robust failure detection, event replication, and secure membership.

---

## Completed Features

### 🚀 Distributed Core
- [x] **Goblin Project Setup**: Separate Go module, directory structure, and `goblind` daemon.
- [x] **Distributed Event Bus**:
    - [x] Event publication (Local, Cluster, Leader)
    - [x] Intelligent routing based on topic prefixes
    - [x] Async event handlers
- [x] **Node Discovery**: Serf-based member discovery and management.
- [x] **Consensus Layer**: Raft-based distributed consensus for cluster state.
- [x] **Event Replication**: Gossip protocol for cluster-wide events.
- [x] **Namespacing & Tagging**: Multi-tenancy support for the event bus.

### 🛠️ Developer Experience
- [x] **Dev Environment**: Reproducible `flake.nix` with all dependencies.
- [x] **Hermetic Builds**: Enforced toolchain via `Magefile.go` check.
- [x] **Multi-Node Support**: `goblinctl` flags for local cluster formation.
- [x] **Documentation System**: Shared documentation generation with GAPI.

---

## Roadmap

### Immediate Priorities
- [ ] **State Management**: Distributed Key/Value store integration (on top of Raft).
- [ ] **Job Scheduling**: Distributed task placement strategies.
- [ ] **Agent Migration**: Moving agents between nodes in response to failure or load.

---

## 🎉 Session Accomplishments (2025-12-14)

### Goblin (12 Features)
1. ✅ **Project Setup** - go.mod, structure, README
2. ✅ **Distributed Event Bus** - Core pub/sub
3. ✅ **Routing Logic** - Topic-based strategies
4. ✅ **Comprehensive Tests** - 6/6 passing
5. ✅ **Working Daemon** - goblind operational
6. ✅ **Node Discovery** - Serf integration
7. ✅ **Consensus Layer** - Raft integration with FSM
8. ✅ **Event Replication** - Gossip/Raft backed bus
9. ✅ **Dev Environment** - Nix flake & documentation site
10. ✅ **Multi-Node Support** - `goblinctl` flags for local clusters
11. ✅ **Namespacing & Tagging** - Daemon grouping and ACL control
12. ✅ **Documentation Integration** - HTML & Man page generation
13. ✅ **Hermetic Builds** - Parity with GAPI build system
