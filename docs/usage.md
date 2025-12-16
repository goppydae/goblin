# Goblin Usage Guide

This guide covers how to run Goblin in various configurations.

## Prerequisites

- **Goblin Binary**: Built via `nix develop -c mage build`.
- **Ports**: Ensure necessary ports aren't blocked by firewalls.

## Single Node (Development)

Start a single node with default settings:

```bash
# Uses hostname as ID, listening on 127.0.0.1:7946 (Serf) and :8300 (Raft)
./bin/goblind
```

## Multi-Node Local Cluster

To simulate a 3-node cluster on a single machine, you must use different ports and data directories.

### Node 1 (Bootstrap)
Starts the first node.

```bash
./bin/goblind \
  --id node1 \
  --serf-port 7946 \
  --raft-addr 127.0.0.1:8300 \
  --data ./data/node1
```

### Node 2
Joins Node 1.

```bash
./bin/goblind \
  --id node2 \
  --serf-port 7947 \
  --raft-addr 127.0.0.1:8301 \
  --data ./data/node2 \
  --join 127.0.0.1:7946
```

### Node 3
Joins cluster via Node 1.

```bash
./bin/goblind \
  --id node3 \
  --serf-port 7948 \
  --raft-addr 127.0.0.1:8302 \
  --data ./data/node3 \
  --join 127.0.0.1:7946
```

## Command Line Reference

| Flag | Default | Description |
|---|---|---|
| `--id` | `hostname` | Unique identifier for the node. Must be stable across restarts. |
| `--serf-addr` | `127.0.0.1` | Bind address for Serf gossip. |
| `--serf-port` | `7946` | Bind port for Serf gossip (UDP/TCP). |
| `--raft-addr` | `127.0.0.1:8300`| Bind address for Raft consensus (TCP). |
| `--data` | `./data/raft` | Directory to store Raft logs and snapshots. |
| `--join` | `""` | Address of an existing cluster member to join (e.g., `192.168.1.10:7946`). |

## Monitoring

Serf logs will appear in stdout/stderr indicating member joins/leaves.
The distributed event bus will log `[cluster]` events.

To check local status:
```bash
./bin/goblinctl status
```
