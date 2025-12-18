# Goblin Usage Guide

This guide covers how to run Goblin in various configurations.

## Prerequisites

- **Goblin Binary**: Built via `nix develop -c mage build`.
- **Ports**: Ensure necessary ports aren't blocked by firewalls.

## Single Node (Development)

Start a single node with default settings:

```bash
# Uses hostname as ID, listening on 127.0.0.1:29010 (Serf) and :29020 (Raft)
./bin/goblind
```

## Multi-Node Local Cluster

To simulate a 3-node cluster on a single machine, you must use different ports and data directories.

### Node 1 (Bootstrap)

Starts the first node.

```bash
./bin/goblind \
  --id node1 \
  --serf-addr 127.0.0.1:29010 \
  --raft-addr 127.0.0.1:29020 \
  --data ./data/node1
```

### Node 2

Joins Node 1.

```bash
./bin/goblind \
  --id node2 \
  --serf-addr 127.0.0.1:29011 \
  --raft-addr 127.0.0.1:29021 \
  --data ./data/node2 \
  --join 127.0.0.1:29010
```

### Node 3

Joins cluster via Node 1.

```bash
./bin/goblind \
  --id node3 \
  --serf-addr 127.0.0.1:29012 \
  --raft-addr 127.0.0.1:29022 \
  --data ./data/node3 \
  --join 127.0.0.1:29010
```

## Command Line Reference

| Flag             | Default           | Description                                                                 |
| ---------------- | ----------------- | --------------------------------------------------------------------------- |
| `--id`           | `hostname`        | Unique identifier for the node. Must be stable across restarts.             |
| `--serf-addr`    | `127.0.0.1:29010` | Bind address for Serf gossip (host:port, UDP/TCP).                          |
| `--raft-addr`    | `127.0.0.1:29020` | Bind address for Raft consensus (host:port, QUIC).                          |
| `--api-addr`     | `127.0.0.1:29000` | Bind address for QUIC API (host:port, UDP).                                 |
| `--tls-ca`       | `""`              | Path to CA certificate for API TLS verification.                            |
| `--tls-insecure` | `false`           | Skip API TLS verification (INSECURE).                                       |
| `--data`         | `./data/raft`     | Directory to store Raft logs and snapshots.                                 |
| `--join`         | `""`              | Address of an existing cluster member to join (e.g., `192.168.1.10:29010`). |

## Monitoring

Serf logs will appear in stdout/stderr indicating member joins/leaves.
The distributed event bus will log `[cluster]` events.

To check local status:

```bash
./bin/goblinctl status --tls-insecure
```

> **Note**: Default `goblind` uses self-signed certs. Use `--tls-insecure` locally. For production, provide a CA via `--tls-ca`.
