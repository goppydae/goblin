# GoPPydae Ecosystem: Roadmap

**Last Updated**: December 16, 2025\
**Status**: Phase 0 & 2 Complete, Planning Phase 3

______________________________________________________________________

## Executive Summary

The GoPPydae ecosystem has achieved a significant architectural milestone with the completion of GAPI Phase 0 (core library refactor) and Goblin Phase 2 (in-process agent integration). This roadmap outlines the strategic path forward, focusing on global agent scheduling, operational excellence, and ecosystem maturity.

______________________________________________________________________

## Completed Phases ✅

### Phase 0: GAPI Core Library Refactor

**Status**: ✅ Complete (Dec 16, 2025)

**Achievements**:

- Migrated 4 packages from `internal/` to `core/*`
- Exported stable public APIs (state, cgroups, lifecycle, agentmgr)
- Enabled library-first architecture
- Maintained backward compatibility with standalone `gapid`

**Impact**: Foundation for hybrid ecosystem model

______________________________________________________________________

### Phase 2: Goblin GAPI Integration

**Status**: ✅ Complete (Dec 16, 2025)

**Achievements**:

- Single-executable architecture (`goblind`)
- In-process agent management (zero network overhead)
- Unified TUI displaying cluster + jobs + local agents
- Opt-in local agent management (`--enable-local-agents`)
- Full cluster test verification passed

**Impact**: Eliminated separate `gapid` daemon requirement for Goblin users

______________________________________________________________________

## Phase 3: Global Agent Scheduling 🎯

**Status**: 📋 Planned\
**Priority**: High\
**Estimated Effort**: 3-4 weeks\
**Target**: Q1 2026

### Overview

Transform agents from node-local entities into cluster-managed resources with redundancy, failover, and intelligent placement strategies.

### Goals

1. **Agent Redundancy**: Run N instances of critical agents across cluster
1. **Automatic Failover**: Re-schedule agents when nodes fail
1. **Intelligent Placement**: Spread, binpack, or affinity-based scheduling
1. **Global Registry**: Single source of truth for agent state across cluster
1. **Health Monitoring**: Proactive detection and remediation

### Use Cases

#### Algorithmic Trading

```
Agent: risk-calculator
Replicas: 3
Strategy: spread (different availability zones)
Affinity: gpu=true
```

**Benefit**: High availability for mission-critical calculations

#### Data Pipeline

```
Agent: stream-processor
Replicas: 5
Strategy: binpack (resource efficiency)
Anti-affinity: node-type=spot (avoid spot instance risk)
```

**Benefit**: Cost-efficient resource utilization with reliability

#### Real-time Analytics

```
Agent: metrics-aggregator
Replicas: 2
Strategy: spread
Constraints: memory > 16GB
```

**Benefit**: Load balancing with hardware requirements

______________________________________________________________________

## Phase 3 Implementation Plan

### 3.1 Data Model

#### AgentSpec (Global Definition)

```go
type AgentSpec struct {
    ID           string            // Unique agent identifier
    Type         string            // Agent type (e.g., "risk-calculator")
    Language     string            // python, go, timer
    Module       string            // Python module or Go binary path
    
    // Scheduling
    Replicas     int               // Desired instance count
    Strategy     string            // "spread" | "binpack" | "affinity"
    Constraints  map[string]string // Node requirements (cpu, memory, gpu)
    Affinity     []AffinityRule    // Prefer/require co-location
    AntiAffinity []AffinityRule    // Avoid co-location
    
    // Resource Requirements
    Resources    ResourceSpec      // CPU, memory limits
    
    // Dependencies
    Requires     []string          // Hard dependencies
    Wants        []string          // Soft dependencies
}
```

#### AgentInstance (Scheduled Instance)

```go
type AgentInstance struct {
    SpecID       string            // References AgentSpec.ID
    InstanceID   string            // Unique instance ID (spec-id-node-id-seq)
    NodeID       string            // Assigned node
    State        string            // "pending" | "running" | "failed" | "migrating"
    StartedAt    time.Time
    Health       HealthStatus
}
```

#### HealthStatus

```go
type HealthStatus struct {
    LastCheck    time.Time
    Status       string            // "healthy" | "degraded" | "unhealthy"
    Failures     int               // Consecutive failures
    Message      string
}
```

______________________________________________________________________

### 3.2 Scheduler Architecture

```
┌──────────────────────────────────────────────┐
│         Global Agent Scheduler               │
├──────────────────────────────────────────────┤
│  ┌────────────────────────────────────────┐  │
│  │   Agent Spec Registry (Raft/KV)        │  │
│  │   - Desired state (specs)              │  │
│  │   - Actual state (instances)           │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │   Reconciliation Loop                  │  │
│  │   - Compare desired vs actual          │  │
│  │   - Trigger placement decisions        │  │
│  │   - Handle failures                    │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │   Placement Engine                     │  │
│  │   - Spread algorithm                   │  │
│  │   - Binpack algorithm                  │  │
│  │   - Affinity/anti-affinity             │  │
│  │   - Constraint satisfaction            │  │
│  └────────────────────────────────────────┘  │
│                                              │
│  ┌────────────────────────────────────────┐  │
│  │   Health Checker                       │  │
│  │   - Poll agent state from nodes        │  │
│  │   - Detect failures                    │  │
│  │   - Trigger remediation                │  │
│  └────────────────────────────────────────┘  │
└──────────────────────────────────────────────┘
                     │
                     ↓
         ┌───────────────────────┐
         │  Node Agent Managers  │
         │  (GAPI Core)          │
         └───────────────────────┘
```

______________________________________________________________________

### 3.3 Implementation Steps

#### Step 1: Agent Spec Storage

**Goal**: Store agent specifications in Raft/KV store

```go
// KV Keys
/agents/specs/{agent-id}           -> AgentSpec (JSON)
/agents/instances/{instance-id}    -> AgentInstance (JSON)
/agents/assignments/{node-id}/{instance-id} -> InstanceID
```

**Tasks**:

- [ ] Define `AgentSpec` protobuf schema
- [ ] Add `RegisterAgent` RPC method
- [ ] Add `UpdateAgent` RPC method
- [ ] Add `DeleteAgent` RPC method
- [ ] Store specs in distributed KV

#### Step 2: Placement Engine

**Goal**: Implement scheduling algorithms

**Spread Strategy**:

```go
func (s *Scheduler) PlaceSpread(spec AgentSpec, nodes []Node) []Placement {
    // Distribute replicas evenly across nodes
    // Avoid placing multiple instances on same node
}
```

**Binpack Strategy**:

```go
func (s *Scheduler) PlaceBinpack(spec AgentSpec, nodes []Node) []Placement {
    // Maximize resource utilization
    // Fill nodes before spreading
}
```

**Tasks**:

- [ ] Implement spread algorithm
- [ ] Implement binpack algorithm
- [ ] Add constraint checking (cpu, memory, tags)
- [ ] Add affinity rule evaluation
- [ ] Write unit tests for each strategy

#### Step 3: Reconciliation Loop

**Goal**: Ensure actual state matches desired state

```go
func (s *GlobalAgentScheduler) Reconcile() {
    for _, spec := range s.GetAgentSpecs() {
        actual := s.GetInstances(spec.ID)
        
        if len(actual) < spec.Replicas {
            // Scale up: place new instances
            placements := s.PlacementEngine.Place(spec, spec.Replicas - len(actual))
            for _, p := range placements {
                s.CreateInstance(spec.ID, p.NodeID)
            }
        } else if len(actual) > spec.Replicas {
            // Scale down: remove excess instances
            s.RemoveInstances(spec.ID, len(actual) - spec Replicas)
        }
        
        // Check health and replace failed instances
        for _, instance := range actual {
            if instance.Health.Status == "unhealthy" {
                s.ReplaceInstance(instance)
            }
        }
    }
}
```

**Tasks**:

- [ ] Implement reconciliation loop (runs every 10s)
- [ ] Add scale-up logic
- [ ] Add scale-down logic
- [ ] Add failure detection and replacement
- [ ] Add rate limiting (avoid thundering herd)

#### Step 4: RPC Layer

**Goal**: Expose agent scheduling via RPC

**New RPC Methods**:

```go
func (s *SchedulerRPC) RegisterGlobalAgent(spec *AgentSpec, resp *string) error
func (s *SchedulerRPC) ListGlobalAgents(req *struct{}, resp *[]AgentSpec) error
func (s *SchedulerRPC) GetAgentInstances(agentID *string, resp *[]AgentInstance) error
func (s *SchedulerRPC) ScaleAgent(req *ScaleRequest, resp *string) error
func (s *SchedulerRPC) DeleteAgent(agentID *string, resp *string) error
```

**Tasks**:

- [ ] Define RPC protobuf messages
- [ ] Implement RPC methods
- [ ] Register handlers in QUIC server
- [ ] Add authentication/authorization

#### Step 5: Node Agent Manager Integration

**Goal**: Execute placement decisions on nodes

**Flow**:

1. Leader schedules agent instance to node
1. Leader sends CREATE_INSTANCE RPC to target node
1. Node's local GAPI agent manager starts agent
1. Node reports instance state back to leader

**Tasks**:

- [ ] Add `CreateAgentInstance` RPC to node
- [ ] Add `DeleteAgentInstance` RPC to node
- [ ] Add state reporting (heartbeat every 5s)
- [ ] Handle network partitions gracefully

#### Step 6: CLI Commands

**Goal**: User-friendly CLI for global agents

```bash
# Register global agent
goblinctl agent register ./spec.yaml

# List global agents
goblinctl agent list

# Show agent instances across cluster
goblinctl agent instances risk-calculator

# Scale agent
goblinctl agent scale risk-calculator --replicas=5

# Delete agent
goblinctl agent delete risk-calculator
```

**Tasks**:

- [ ] Add `goblinctl agent` subcommand
- [ ] Implement `register`, `list`, `instances`, `scale`, `delete`
- [ ] Add YAML spec validation
- [ ] Add dry-run mode

#### Step 7: TUI Integration

**Goal**: Visualize global agents in unified TUI

**New TUI Section**:

```
Global Agents:
  NAME              REPLICAS   HEALTHY   STRATEGY   NODES
  risk-calculator   3/3        3         spread     node-1, node-3, node-5
  stream-processor  5/5        4         binpack    node-2, node-2, node-4, node-4, node-5
  metrics-agg       2/2        2         spread     node-1, node-4
```

**Tasks**:

- [ ] Add "Global Agents" tab to TUI
- [ ] Display agent specs and health
- [ ] Show instance distribution across nodes
- [ ] Add real-time updates

______________________________________________________________________

### 3.4 Testing Strategy

#### Unit Tests

- [ ] Placement algorithm tests (spread, binpack)
- [ ] Constraint satisfaction tests
- [ ] Affinity rule evaluation tests
- [ ] Reconciliation logic tests

#### Integration Tests

- [ ] 3-node cluster agent scheduling
- [ ] Node failure and failover
- [ ] Network partition recovery
- [ ] Scale up/down scenarios

#### Chaos Tests

- [ ] Random node failures
- [ ] Leader election during scheduling
- [ ] Concurrent agent registration
- [ ] Resource exhaustion handling

______________________________________________________________________

### 3.5 Success Metrics

| Metric                 | Target | Measurement                               |
| ---------------------- | ------ | ----------------------------------------- |
| Failover Time          | < 30s  | Time from node failure to agent restart   |
| Placement Latency      | < 1s   | Time to schedule new instance             |
| Health Check Interval  | 5s     | Frequency of agent health checks          |
| Reconciliation Loop    | 10s    | Frequency of desired vs actual comparison |
| Max Agents per Cluster | 1000+  | Scalability target                        |

______________________________________________________________________

## Phase 4: Operational Excellence

**Status**: 📋 Planned\
**Priority**: Medium\
**Estimated Effort**: 2-3 weeks\
**Target**: Q1 2026

### Goals

1. **Observability**: Rich metrics, logging, tracing
1. **Debugging**: Agent logs centralized and searchable
1. **Backup/Restore**: Cluster state backup and recovery
1. **Upgrades**: Rolling updates without downtime

### Key Features

#### 4.1 Metrics Collection

```go
// Agent-level metrics
agent_start_total{id, node}
agent_stop_total{id, node}
agent_restart_total{id, node}
agent_failure_total{id, node}
agent_uptime_seconds{id, node}

// Scheduler metrics
scheduler_placement_duration_seconds
scheduler_reconciliation_duration_seconds
scheduler_queue_depth
```

#### 4.2 Centralized Logging

- Forward agent logs to Loki
- Tag logs with: agent_id, node_id, instance_id
- Enable log search via CLI: `goblinctl logs <agent-id>`

#### 4.3 Distributed Tracing

- Trace agent lifecycle events (start, stop, migrate)
- Trace RPC calls across cluster
- Integration with OpenTelemetry

#### 4.4 Backup & Restore

```bash
# Backup cluster state
goblinctl backup create /tmp/cluster-backup.tar.gz

# Restore from backup
goblinctl backup restore /tmp/cluster-backup.tar.gz
```

______________________________________________________________________

## Phase 5: Advanced Features

**Status**: 📋 Planned\
**Priority**: Low\
**Estimated Effort**: varies\
**Target**: Q2 2026+

### 5.1 Agent Canary Deployments

```yaml
agent:
  id: risk-calculator
  replicas: 10
  canary:
    replicas: 2
    version: v2.0.0
    rollout_strategy: gradual
```

### 5.2 Agent Auto-Scaling

```yaml
agent:
  id: stream-processor
  autoscaling:
    min_replicas: 3
    max_replicas: 20
    target_cpu_percent: 70
```

### 5.3 Multi-Cluster Federation

- Agents scheduled across multiple Goblin clusters
- Global load balancing
- Cross-cluster failover

### 5.4 Agent Marketplace

- Community-contributed agents
- Version management
- Dependency resolution

______________________________________________________________________

## Timeline Summary

```
Q4 2025 (Dec)   ✅ Phase 0 & 2 Complete
Q1 2026 (Jan)   🎯 Phase 3: Global Agent Scheduling (Weeks 1-4)
Q1 2026 (Feb)   📊 Phase 4: Operational Excellence (Weeks 5-8)
Q2 2026 (Mar+)  🚀 Phase 5: Advanced Features (ongoing)
```

______________________________________________________________________

## Success Criteria

### Phase 3

- [ ] Agent specs stored in distributed KV
- [ ] 3 placement strategies implemented (spread, binpack, affinity)
- [ ] Reconciliation loop running in leader
- [ ] Failover time < 30s
- [ ] CLI commands functional
- [ ] TUI shows global agents
- [ ] Integration tests passing
- [ ] Documentation complete

### Phase 4

- [ ] Metrics exported to Prometheus
- [ ] Logs forwarded to Loki
- [ ] Tracing operational
- [ ] Backup/restore working
- [ ] Rolling upgrades tested

______________________________________________________________________

## Risk Mitigation

| Risk                    | Impact | Mitigation                                      |
| ----------------------- | ------ | ----------------------------------------------- |
| Network partitions      | High   | Implement partition tolerance in reconciliation |
| Leader election delays  | Medium | Add leader lease mechanism                      |
| Resource exhaustion     | High   | Enforce resource quotas per node                |
| Agent dependency cycles | Medium | Validate dependency graph before scheduling     |
| Data consistency        | High   | Use Raft for all state mutations                |

______________________________________________________________________

## Dependencies

### External

- Raft (consensus) - ✅ Available
- Serf (membership) - ✅ Available
- QUIC (transport) - ✅ Available
- Prometheus (metrics) - ⏳ Needs integration
- Loki (logging) - ⏳ Needs integration

### Internal

- GAPI core library - ✅ Complete
- Goblin scheduler - ✅ Complete
- Distributed KV store - ✅ Complete
- QUIC RPC layer - ✅ Complete

______________________________________________________________________

## Next Actions (Immediate)

1. **Create GitHub Issues**: Break down Phase 3 into trackable issues
1. **Design Review**: Agent spec schema and placement algorithms
1. **Prototype**: Simple spread scheduler (proof of concept)
1. **Documentation**: API design for global agent scheduling
1. **Team Alignment**: Review roadmap with stakeholders

______________________________________________________________________

## Conclusion

The GoPPydae ecosystem has a clear path toward production-grade global agent scheduling. Phase 3 represents a significant evolution from node-local to cluster-global agent management, unlocking high-availability and intelligent resource utilization use cases.

**Recommendation**: Begin Phase 3 implementation in January 2026 with a focus on MVP (spread strategy, basic reconciliation) before expanding to advanced features.

______________________________________________________________________

**Document Version**: 1.0\
**Maintained By**: GoPPydae Engineering Team\
**Next Review**: January 2026
