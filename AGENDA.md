# Goblin Agenda

<!-- This file tracks the immediate development plan for the Goblin project. -->

## Active Hypotheses
- None at this phase.

## Blockers
- None.

## Deferred Risks
- **SCHED-01**: Global scheduling reliability during network partitions is unknown.

## Phase 1: Secure Control Plane (Current Focus)
**Goal**: Unify all CLI and Client operations over a secured QUIC transport.

### 1. Unified Transport
- [x] **Action**: Migrate `goblinctl publish` (Serf) to use Goblin RPC (QUIC).
- **Benefit**: Removes direct Serf dependency from CLI, routing all traffic through one port/protocol.

### 2. Control Plane Security
- [x] **Action**: Enforce TLS verification for the QUIC RPC client.
- **Requirement**: Require `--tls-ca` (prod) or explicit `--tls-insecure` (dev).

## Phase 2: Secure Data Plane (Future)
**Goal**: Migrate internal cluster traffic to QUIC for full "All-QUIC" transport.

### 3. QUIC Transport Adapters
- [x] **Action**: Implement `hashicorp/raft.NetworkTransport` over QUIC.
- [x] **Action**: Implement `hashicorp/serf.NetworkTransport` over QUIC.
- **Benefit**: Single UDP port for all traffic, encrypted pipes by default.

---

## Other Items

### 1. Integrate GAPI Core Libraries
**Goal**: Enable local agent management via GAPI core (in-process, no separate daemon).

- [x] **Action**: Add GAPI core imports to Goblin supervisor
- [x] **Action**: Initialize GAPI agent manager in `goblind` if `--enable-local-agents` flag set
- [x] **Action**: Add `SchedulerRPC.ListLocalAgents()` RPC method
- [x] **Action**: Register handler in QUIC RPC server
- [x] **Action**: Update unified TUI controller to display local agents
- **Status**: ✅ Complete (Dec 16, 2025)

### 2. Unified Controller Local Agents
**Goal**: Display local agent status in `goblinctl tui`.

- [x] **Action**: Wire `UnifiedController` to call `ListLocalAgents` RPC
- [x] **Action**: Display local agents alongside cluster members and jobs
- **Status**: ✅ Complete (Dec 16, 2025)

---

## Phase 3: CLI Harmonization and Port Refactoring

### 1. Port Configuration Refactoring
- [x] **Action**: Update default ports to non-standard values.
- [x] **API**: `29000`
- [x] **Serf**: `29010`
- [x] **Raft**: `29020`
- **Status**: ✅ Complete (Dec 17, 2025)

### 2. CLI Command Harmonization
- [x] **Action**: Refactor `goblinctl` into namespaces (`cluster`, `agent`).
- [x] **Action**: Move job and cluster operations under `cluster` namespace.
- [x] **Action**: Flatten GAPI agent commands under `agent` namespace.
- [x] **Action**: Move global agent specs under `cluster agent`.
- **Status**: ✅ Complete (Dec 17, 2025)
