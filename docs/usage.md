# Goblin Usage Guide

How to run Goblin. For the full flag and command surface, see the
[CLI Reference](cli-reference.md).

## Prerequisites

- **Binaries**: built with `nix develop -c mage build`, producing
  `bin/goblind` and `bin/goblinctl`.
- **Ports**: one UDP port per node, `31415` by default. Everything -
  gossip, consensus, RPC, the embedded kernel, and checkpoint transfer
  - shares it, routed by TLS ALPN. There is no separate Serf or Raft
  port to open.

## Single node

```bash
./bin/goblind start
```

Uses the hostname as the node ID and binds `127.0.0.1:31415`. With no
`--join`, the node bootstraps a cluster of one.

## Multi-node cluster on one machine

Each node needs its own port and its own data directory. `--join`
points at the first node's control-plane address.

```bash
./bin/goblind start --id node1 --listen-addr 127.0.0.1:31415 --data ./data/node1
```

```bash
./bin/goblind start --id node2 --listen-addr 127.0.0.1:31416 --data ./data/node2 --join 127.0.0.1:31415
```

```bash
./bin/goblind start --id node3 --listen-addr 127.0.0.1:31417 --data ./data/node3 --join 127.0.0.1:31415
```

## Multi-node cluster across machines

Bind to all interfaces and advertise the address peers should dial.
`--advertise-addr` takes a bare HOST; the port follows the listen
address.

```bash
./bin/goblind start --id node1 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.1 --data /var/lib/goblin/raft
```

```bash
./bin/goblind start --id node2 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.2 --data /var/lib/goblin/raft --join 10.0.0.1:31415
```

For a fixed set of seeds, `--bootstrap-expect N` on every seed makes
them wait until N of them are visible and then elect one to bootstrap,
so no node has to be designated by hand.

## Checking the cluster

```bash
./bin/goblinctl cluster status --tls-insecure
```

Note the path: cluster verbs live under `goblinctl cluster`. A bare
`goblinctl status` is not a command - and because cobra prints help and
exits 0 for a path that does not exist, the mistake looks like it
worked.

## Scheduling work

```bash
./bin/goblinctl cluster agent register ./spec.yaml --tls-insecure
```

```bash
./bin/goblinctl cluster agent instances --tls-insecure
```

## Moving a running process

```bash
./bin/goblinctl cluster migrate-instance <instance-uuid> node2 --tls-insecure
```

The process is checkpointed with CRIU, its memory image is transferred,
and it is restored on the destination under the same instance UUID.
This needs CRIU on both nodes and the capabilities the NixOS module
grants; see `nix/module.nix`.

`cluster migrate <job-id> <node>` is a different operation - it
reassigns a job, and the process does not survive.

## Monitoring

Serf membership joins and leaves appear on stdout, as do distributed
event bus events. `--log-format json` switches to structured output,
and `--metrics-addr` exposes Prometheus metrics.

> **Note**: with no `--tls-cert` and `--tls-key`, `goblind` generates an
> ephemeral self-signed certificate and does not verify peers. Use
> `--tls-insecure` on the client locally. For production, supply real
> certificates and a CA.
