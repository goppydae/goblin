---
primary_domain: software
deliverable: Distributed orchestration and policy layer for GoPPydae kernel.
first_milestone_done: Multi-node cluster coordination and policy-driven reconciliation functional.
project_name: Goblin
milestone: v1.0
intent_id: AG-000000
status: in-progress
constraints:
  - Must remain policy-driven and cluster-aware
  - Must use idiomatic Go
  - Must import GAPI as a library
non_goals:
  - Local lifecycle mechanism (handled by GAPI)
---

# Goblin: Distributed Orchestrator

Goblin coordinates multiple GAPI instances across a cluster. It handles leader election, routing, and multi-node scheduling using global intent and reconciliation patterns.

## Roadmap
- [x] Initial design
- [ ] v0.1 release
- [ ] v1.0 milestone
