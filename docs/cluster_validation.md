# Goblin Cluster Validation Checklist

This checklist tracks the validation steps for ensuring deterministic cluster behavior, explicit failure modes, and operator-grade introspection.

## 0) Preconditions and Known-Good Baselines

- [ ] Capture expected cluster topology (node count, roles, leader-eligible nodes)
- [ ] Confirm time synchronization (NTP/chrony, monotonic clocks)
- [ ] Confirm DNS, hostnames, and IP stability
- [ ] Confirm firewall rules and required ports (bidirectional)
- [ ] Confirm storage expectations (local vs shared, permissions, free space)
- [ ] Confirm configuration distribution and update mechanism

## 1) Node Bring-up Validation

- [ ] Verify all nodes are running
- [ ] Verify all nodes are in the cluster
- [ ] Verify all nodes are healthy
- [ ] Verify all nodes are in sync
- [ ] Verify all nodes are in the same state
- [ ] Verify all nodes are running the same version
- [ ] Verify leader election correctness (exactly one leader when applicable)
- [ ] Verify node identity stability across restarts
- [ ] Verify node introspection output (ID, version, hash, capabilities)

## 2) Cluster Control Plane Correctness

- [ ] Validate control-plane messaging end-to-end
- [ ] Confirm separation of logs and control signals
- [ ] Validate event/topic routing across nodes
- [ ] Validate backpressure handling under load
- [ ] Validate timeout and retry behavior

## 3) Security and Integrity

- [ ] Verify schema hash consistency across all nodes
- [ ] Verify node and operator identity keys are present and correct
- [ ] Verify signed manifest validation at runtime (if enabled)
- [ ] Verify secret handling (decrypt at start, never log plaintext)
- [ ] Verify failure behavior on invalid or missing credentials

## 4) Agent Deployment (Happy Path)

- [ ] Deploy multiple agents to the cluster
- [ ] Verify agents are running
- [ ] Verify agents are registered in the cluster
- [ ] Verify agents are healthy
- [ ] Verify agents are in sync
- [ ] Verify agents are in the same state
- [ ] Verify agent lifecycle phases (Init → Start → Stop → Reload)
- [ ] Verify dependency semantics between agents (if applicable)
- [ ] Verify agent introspection output is stable and comparable

## 5) Failure and Chaos Scenarios

- [ ] Kill an agent process and verify supervisor reconciliation
- [ ] Restart a node and verify clean rejoin and state resync
- [ ] Introduce network partitions and verify explicit degraded behavior
- [ ] Kill the leader and verify re-election correctness
- [ ] Introduce latency or jitter and verify slow-node detection

## 6) Upgrades and Compatibility

- [ ] Perform rolling upgrades without cluster downtime
- [ ] Verify version skew policy (allowed vs denied)
- [ ] Verify schema compatibility enforcement
- [ ] Verify downgrade behavior (supported or explicitly blocked)

## 7) Observability and Operator Workflow

- [ ] Verify structured logging consistency across nodes and agents
- [ ] Validate cluster status and agent status commands
- [ ] Define and collect core metrics (election time, convergence time, restart time)
- [ ] Verify audit trail for operator actions

## 8) Definition of Done

- [ ] Every check has:
      - A command or test
      - Expected output
      - Failure signature
      - Fix pointer
- [ ] A deterministic smoke-test script validates a fresh cluster quickly
- [ ] A chaos-lite test suite exists and is runnable locally or in CI
