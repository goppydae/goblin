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
- [x] **State Management**: Distributed Key/Value store integration (on top of Raft).
- [x] **Job Scheduling**: Distributed task placement strategies.
- [x] **Agent Migration**: Moving agents between nodes in response to failure or load.

---

## 🎉 Session Accomplishments (2025-12-15)

### Goblin (Job Scheduling Release)
1. ✅ **Scheduler Core** - Filter & Assign logic
2. ✅ **Job Submission API** - gRPC/HTTP endpoints
3. ✅ **KV Store** - Raft-based distributed storage
4. ✅ **Agent Lifecycle** - Gapi client integration
5. ✅ **CLI Tooling** - `schedule` and `status` commands
6. ✅ **CI/CD Pipeline** - GitHub Actions with Gapi integration
7. ✅ **E2E Verification** - Multi-node cluster scheduling tests

### Breakdown
- **KV Store**: Implemented a replicated finite-state machine (FSM) over Raft.
- **Scheduling**: Simple random placement strategy (extensible interface).
- **Integration**: Full source-to-binary verification link with Gapi.

---

## Proposed Enhancements

### Resource-Aware Scheduling
- [ ] **StrategyLeastLoaded/BinPack**: Move beyond random scheduling by tracking node capacity and load.
- [ ] **Job Constraints**: Support node labels, affinity/anti-affinity rules.

### Security Hardening
- [ ] **mTLS**: Enforce strict mTLS for all Raft/Serf/Control plane traffic (remove `InsecureSkipVerify`).
- [ ] **CA Rotation**: Automated certificate management.

### Observability
- [ ] **Cluster Metrics**: Expose Raft latency, Serf member counts, and job placement stats via Prometheus.
- [ ] **Distributed Logs**: Centralized log aggregation for all cluster nodes.

### Enhanced Job Specs
- [ ] **RestartPolicy**: Explicit control over job restart behavior (Always, OnFailure, Never).
- [ ] **Environment Variables**: Inject `Env` map into running agents.

