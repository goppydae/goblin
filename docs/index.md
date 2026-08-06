# Goblin

**Goblin** extends GAPI with distributed orchestration.

GAPI is the kernel: single-node process supervision, mechanism only.
Goblin is the policy layer around it - which node runs what, and what
happens when that changes.

## Features

- **Cluster membership**: Serf-based discovery, carried over QUIC.
- **Consensus**: Raft leader election and a replicated instance table.
- **Distributed events**: a unified event bus across the cluster.
- **Capability tokens**: every mutating verb is authorized against a
  named subject and audited through Raft.
- **Live migration**: move a running process between nodes with its
  memory intact, under an unchanged instance UUID.
- **One control-plane port**: GAPI, RPC, gossip, Raft and checkpoint
  transfer share a single address, routed by TLS ALPN.

## Guides

- [Usage](usage.md) - start a cluster
- [Architecture](architecture.md) - membership, consensus, event bus, transport
- [CLI Reference](cli-reference.md) - every `goblind` flag and `goblinctl` command

How Goblin and GAPI divide the work is documented in the goppydae-docs
repository.
