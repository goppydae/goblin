# Goblin CLI Reference

Complete reference for `goblind` (daemon) and `goblinctl` (CLI client).

---

## `goblind` - Goblin Distributed Supervisor Daemon

**Description**: Starts the Goblin supervisor daemon with cluster management and optional local agent management.

### Usage

```bash
goblind [flags]
```

### Flags

#### Cluster Configuration

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--id` | string | hostname | Unique Node ID |
| `--serf-addr` | string | `127.0.0.1:9001` | Serf bind address (host:port) |
| `--serf-advertise-addr` | string | - | Serf advertise address (if different from bind) |
| `--serf-advertise-port` | int | - | Serf advertise port (if different from bind) |
| `--raft-addr` | string | `127.0.0.1:9002` | Raft bind address (host:port) |
| `--api-addr` | string | `127.0.0.1:9000` | API address |
| `--join` | string | - | Join existing cluster peer (host:port) |

#### Security

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--encrypt` | string | - | Base64 encoded 32-byte secret key for Serf encryption |
| `--tls-cert` | string | - | Path to TLS certificate |
| `--tls-key` | string | - | Path to TLS private key |
| `--tls-ca` | string | - | Path to CA certificate for mTLS |

#### Storage

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--data` | string | `./data/raft` | Data directory for Raft log |

### Agent Discovery

Local agents are **always enabled** and discovered using GAPI's standard search paths:
- `./agents/`
- `~/.config/gapi/agents/`
- `/etc/gapi/agents/`

### Examples

#### Single Node (Development)

```bash
goblind --id=dev-node
```

#### Multi-Node Cluster

**Node 1 (Leader)**:
```bash
goblind \
  --id=node-1 \
  --serf-addr=0.0.0.0:7946 \
  --raft-addr=0.0.0.0:8300 \
  --api-addr=0.0.0.0:9000
```

**Node 2 (Follower)**:
```bash
goblind \
  --id=node-2 \
  --serf-addr=0.0.0.0:7946 \
  --raft-addr=0.0.0.0:8300 \
  --api-addr=0.0.0.0:9000 \
  --join=node-1:7946
```

#### With TLS

```bash
goblind \
  --id=secure-node \
  --tls-cert=certs/node.crt \
  --tls-key=certs/node.key \
  --tls-ca=certs/ca.crt \
  --encrypt=$(head -c 32 /dev/urandom | base64)
```

#### With Local Agents

Agents are automatically discovered from standard paths.
Place agent manifests in:
- `./agents/`
- `~/.config/gapi/agents/`
- `/etc/gapi/agents/`

```bash
# Agents discovered automatically
goblind --id=agent-node
```

---

## `goblinctl` - Goblin Control CLI

**Description**: Unified CLI for cluster management, job scheduling, and local agent operations.

### Usage

```bash
goblinctl [command] [flags]
```

### Global Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--api-addr` | string | `127.0.0.1:29000` | API address |
| `--tls-ca` | string | - | Path to CA certificate for API TLS verification |
| `--tls-insecure` | bool | false | Skip API TLS verification (INSECURE) |

---

## Commands

### `goblinctl cluster status`

Show cluster status including members and leader information.

**Usage**:
```bash
goblinctl cluster status [flags]
```

**Example**:
```bash
goblinctl cluster status --api-addr=node-1:9000
```

---

### `goblinctl tui`

Unified cluster and agent TUI (Text User Interface).

**Usage**:
```bash
goblinctl tui [flags]
```

**Features**:
- Real-time cluster member view
- Job status monitoring
- Local agent display (if enabled)
- Event log streaming

**Controls**:
- `Tab`: Switch tabs
- `↑/↓`: Navigate
- `q`: Quit

---

### `goblinctl cluster`

Job and cluster management operations.

#### Subcommands

- `run <job-file.yaml>`: Submit job
- `drain <node-id>`: Drain node
- `migrate <job-id> <node>`: Migrate job
- `status`: Show cluster members
- `publish`: Broadcast event

**Example**:
```bash
goblinctl cluster run test-job.yaml
```

---

### `goblinctl agent`

Local agent management.

**Example**:
```bash
goblinctl agent list
```

---

### `goblinctl cluster publish`

Publish cluster event.

**Usage**:
```bash
goblinctl cluster publish <event> <payload>
```

---

## Quick Reference

### Start 3-Node Cluster

```bash
# Node 1
goblind --id=n1 --serf-addr=0.0.0.0:7946

# Node 2
goblind --id=n2 --serf-addr=0.0.0.0:7947 --api-addr=0.0.0.0:9001 --join=127.0.0.1:7946

# Node 3
goblind --id=n3 --serf-addr=0.0.0.0:7948 --api-addr=0.0.0.0:9002 --join=127.0.0.1:7946
```

### Check Cluster

```bash
goblinctl cluster status
goblinctl tui
```

---

## Environment Variables

```bash
export GOBLIN_ID=node-1
export GOBLIN_ENABLE_LOCAL_AGENTS=true
```

Pattern: `GOBLIN_<FLAG_UPPERCASE>`
