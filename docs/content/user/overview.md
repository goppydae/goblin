---
title: "Overview"
weight: 5
---

# Goblin

`goblind` is a distributed process supervisor. It runs a cluster of nodes
that agree on which agents should be running and where, restarts them
elsewhere when a node fails, and can move a running process between nodes
with its memory intact.

Each node embeds the GAPI kernel in process and supervises its own agents
directly; there is no second daemon and no network hop for local
supervision. What Goblin adds is the decision of *where*, and the machinery
to change that decision safely.

A node exposes one address. Cluster RPC, membership gossip, consensus,
checkpoint transfer and the kernel's own control plane all share it, routed
by TLS ALPN.

`goblinctl` is the control client. It is the only cluster-facing command;
local agent operations use the kernel's own `gapictl` vocabulary.

See the usage guide to bring a cluster up, and the CLI reference for the
full flag and command surface.
