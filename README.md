# Goblin - Distributed Orchestrator for GAPI (GoPPydae Agent Process Infrastructure)

**Goblin** extends [GAPI](https://github.com/goppydae/gapi) with distributed orchestration capabilities, enabling multi-node agent supervision and cluster-wide coordination.

## Overview

While GAPI provides the local runtime library, Goblin is the **Production Daemon**.

It **embeds** GAPI to run agents locally, while adding:

- **Cluster membership** - node discovery via Serf
- **Consensus** - leader election and a replicated instance table via Raft
- **Distributed events** - cluster-wide pub/sub messaging
- **Multi-node coordination** - reconcile desired state across nodes
- **Capability tokens** - every mutating verb authorized against a named
  subject and audited through Raft
- **Live migration** - move a running process between nodes with its
  memory intact, under an unchanged instance UUID
- **One control-plane port** - kernel, RPC, gossip, Raft and checkpoint
  transfer share a single QUIC address, routed by TLS ALPN

**You only need running `goblind` on your servers.** No separate `gapid` is required.

## Architecture

```
+-------------------------------------------------+
|                  Goblin Cluster                 |
+-------------------------------------------------+
|                                                 |
|  Node A (Leader)      Node B          Node C    |
|  +----------------+   +------------+  +-------+ |
|  | GAPI (embedded)|   | GAPI       |  | GAPI  | |
|  | runtime        |   | (embedded) |  | (emb) | |
|  +--------+-------+   +-----+------+  +---+---+ |
|           |                 |             |     |
|  +--------v-----------------v-------------v---+ |
|  |      Distributed Event Bus (Goblin)        | |
|  |  - Cluster-wide pub/sub                    | |
|  |  - Leader-aware routing                    | |
|  |  - Eventual consistency                    | |
|  +--------------------------------------------+ |
+-------------------------------------------------+
```

## Quick Start

```bash
nix develop -c mage build
```

Single node - with no `--join`, it bootstraps a cluster of one:

```bash
./bin/goblind start
```

Inspect it. Note that cluster verbs live under `goblinctl cluster`; a
bare `goblinctl status` is not a command:

```bash
./bin/goblinctl cluster status --tls-insecure
```

For a multi-node cluster see [docs/content/user/usage.md](docs/content/user/usage.md), and for the
full flag and command surface see
[docs/content/reference/](docs/content/reference/).

## Documentation

- **[Usage](docs/content/user/usage.md)**: setup and multi-node guide.
- **[CLI reference](docs/content/reference/)**: every `goblind` flag and
  `goblinctl` command, generated from the cobra trees and gated against them.
- **[Architecture](docs/content/architecture.md)**: Serf, Raft, the event bus, the ALPN registry, live migration.
- **Ecosystem**: how Goblin and GAPI divide the work is documented in the goppydae-docs repository.

## Project Structure

```
goblin/
|-- cmd/
|   |-- goblinctl/        # Control CLI
|   `-- goblind/          # Daemon entry point
|-- core/
|   |-- capability/       # Capability tokens: rights, verbs, revocation
|   |-- cluster/          # Serf membership
|   |-- consensus/        # Raft consensus and the instance FSM
|   |-- eventbus/         # Distributed event bus
|   |-- metrics/          # Prometheus collectors
|   |-- migration/        # Live migration: store, transfer, coordinator
|   |-- scheduler/        # Placement, reconciliation, locators
|   |-- store/            # Distributed KV over Raft
|   `-- transport/        # Shared QUIC listener, ALPN registry
|-- internal/
|   `-- supervisor/       # Daemon wiring and RPC handlers
|-- nix/                  # NixOS module, package, VM tests
|-- proto/goblin/v1/      # Schemas (raft, scheduler, rpc, migration)
|-- docs/                 # Documentation
`-- divergence.jsonl      # Where design and code currently disagree
```

## License

Mozilla Public License 2.0 (MPL-2.0)
