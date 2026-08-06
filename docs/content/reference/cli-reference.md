---
title: "CLI Reference"
weight: 10
---

# Goblin CLI Reference

The single reference for both binaries: `goblind` (the daemon) and
`goblinctl` (the control CLI). Everything here is checked against the
command definitions in `cmd/` and `internal/cli/`; if a flag or verb is
not listed here, it does not exist.

## `goblind` - Distributed Supervisor Daemon

### Usage

```bash
goblind [flags]
```

### One port, many protocols

`goblind` binds a SINGLE control-plane address. GAPI, cluster RPC, Serf
gossip, Raft and checkpoint transfer all share it, routed by TLS ALPN.
There are no separate Serf, Raft or API ports.

| ALPN | Carries |
| ---- | ------- |
| `gapi-quic` | the embedded GAPI kernel's own protocol |
| `goblin-rpc` | `goblinctl` and scheduler RPC |
| `serf-quic` | Serf membership gossip |
| `raft-quic` | the Raft transport stream |
| `goblin-ckpt` | CRIU checkpoint images during live migration |

The registry lives in `core/transport/alpn.go`; the listener's view of
it is `registryALPNs` in `core/transport/listener.go`.

### Flags

#### Cluster

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--id` | hostname | Unique node ID |
| `--listen-addr` | `127.0.0.1:31415` | Single control-plane bind address |
| `--advertise-addr` | bind host | Advertise address if it differs from the bind. A bare HOST, not `host:port` - the port follows the listen address |
| `--join` | | Existing cluster peer to join, as `host:port` |
| `--bootstrap-expect` | `0` | Seed once this many nodes carrying the same value are visible; one is elected to bootstrap. `0` keeps the seed model, where the node with no `--join` bootstraps alone |

#### Storage

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--data` | `./data/raft` | Data directory for the Raft log |

Checkpoint images are written to a `checkpoints/` directory that is a
SIBLING of the Raft directory, not nested inside it: wiping Raft state
must not discard images an in-flight migration still needs.

#### Security

| Flag | Description |
| ---- | ----------- |
| `--tls-cert` | Path to the TLS certificate |
| `--tls-key` | Path to the TLS private key |
| `--tls-ca` | Path to the CA certificate for mTLS |
| `--encrypt` | Base64-encoded 32-byte key for Serf encryption |
| `--production` | Restrict agent discovery to binaries with verified signatures |
| `--agent-verify-key` | Ed25519 public key for agent signature verification (falls back to `$GOBLIN_VERIFY_KEY`) |

Without `--tls-cert` and `--tls-key` the daemon generates an ephemeral
self-signed certificate and does not verify peers. `--production`
refuses to start in that state.

#### PID 1 and lifecycle

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--pid1` | `false` | Run as PID 1: kernel Phase 0 boot before the cluster stack, reversed teardown on shutdown |
| `--no-early-mounts` | `false` | Skip the Phase 0 mount table (container environments) |
| `--shutdown-grace` | `10s` | Per-phase shutdown grace (drain, then agent stop) before forcing |
| `--watchdog-device` | | Hardware watchdog device to keep alive |
| `--watchdog-interval` | `10s` | Watchdog keepalive interval |
| `--network-gate-timeout` | `0` | Block startup until the network agent reports running, failing after this bound. `0` disables the gate |

#### Observability

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--metrics-addr` | | Prometheus metrics listen address (empty disables) |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--log-format` | | `json` or `console` (json in production, console otherwise) |
| `--log-file` | | Also write logs to this file, rotated |
| `--log-loki-url` | | Forward logs to a Loki endpoint |

### Examples

Single node, development:

```bash
goblind start --id node-1
```

Three-node cluster. Every node advertises the one address its peers
dial:

```bash
goblind start --id node-1 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.1 --data /var/lib/goblin/raft
```

```bash
goblind start --id node-2 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.2 --data /var/lib/goblin/raft --join 10.0.0.1:31415
```

```bash
goblind start --id node-3 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.3 --data /var/lib/goblin/raft --join 10.0.0.1:31415
```

With TLS:

```bash
goblind start --id node-1 --tls-cert /etc/goblin/node.crt --tls-key /etc/goblin/node.key --tls-ca /etc/goblin/ca.crt
```

### Agent discovery

Local agents are discovered from the paths GAPI searches.
`GOBLIN_AGENT_PATH` is **prepended** to those paths: it adds
precedence, it does not replace them. To search only what it names,
set `GOBLIN_AGENT_PATH_EXCLUSIVE=1`.

Setting `GOBLIN_AGENT_PATH` alone therefore discovers *more* agents
than the default, never fewer. A deployment that used it to fence a
node to one directory must set the exclusive switch as well.

## `goblinctl` - Control CLI

### Usage

```bash
goblinctl [command] [flags]
```

### Global flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--control-addr` | `127.0.0.1:31415` | Target node's single listen address |
| `--tls-ca` | | CA certificate for API TLS verification |
| `--tls-insecure` | `false` | Skip API TLS verification (INSECURE) |

There is no `--config` flag and no configuration file.

### Command tree

Two agent namespaces exist and they are NOT interchangeable.
`goblinctl agent` is GAPI's LOCAL tooling for agents on one node.
`goblinctl cluster agent` manages cluster-wide agent SPECIFICATIONS
through the leader. Reaching for the wrong one is easy to do and easy
to miss: cobra prints help and exits 0 for a path that does not exist,
so the mistake looks like success.

```text
goblinctl
|-- start                     # run a supervisor from this binary
|-- tui                       # unified cluster + agent TUI
|-- agent                     # LOCAL agents on one node (GAPI tooling)
|   |-- status                #   registered agents
|   |-- lifecycle             #   start/stop/restart
|   |-- build / new / clean   #   agent development
|   |-- verify / crypto       #   signature verification
|   |-- ping / reload         #   daemon interaction
|   |-- shutdown              #   request system shutdown
|   `-- tui                   #   local agent TUI
`-- cluster                   # CLUSTER operations, via the leader
    |-- status                #   members, leader, jobs
    |-- run <file>            #   submit a job (YAML or JSON)
    |-- drain <node>          #   drain all jobs from a node
    |-- migrate <job> <node>  #   REASSIGN a job to another node
    |-- migrate-instance <instance-uuid> <node>
    |                         #   LIVE-MIGRATE a running process (CRIU)
    |-- publish <event> <payload>
    `-- agent                 #   global agent specifications
        |-- register <spec-file>
        |-- list
        |-- get <id>
        |-- delete <id>
        |-- scale <id> <replicas>
        |-- instances [spec-id]
        `-- signal <instance-uuid> <signum>
```

### `goblinctl cluster status`

Members, the current leader, and scheduled jobs.

```bash
goblinctl cluster status --control-addr 10.0.0.1:31415
```

### `goblinctl cluster agent`

Global agent specifications: what should run, and how many replicas.
Writes go through the leader and are committed to Raft.

```bash
goblinctl cluster agent register ./sleeper-spec.yaml
```

```bash
goblinctl cluster agent scale sleeper-spec 3
```

```bash
goblinctl cluster agent instances sleeper-spec
```

`instances` prints one row per scheduled instance: instance UUID, spec
UUID, node, and state.

### `goblinctl cluster migrate` vs `migrate-instance`

These do different things, and the distinction matters.

`migrate <job-id> <to-node>` REASSIGNS a job. Work stops on one node
and starts on another; the process does not survive.

`migrate-instance <instance-uuid> <to-node>` LIVE-MIGRATES a running
process with its memory intact. The instance is checkpointed with CRIU
on its current node, the image is transferred over the `goblin-ckpt`
ALPN, and it is restored on the destination under the SAME instance
UUID. Only its location changes.

```bash
goblinctl cluster migrate-instance 019fab39-1fd4-7134-9783-e5f827406cc9 node-2
```

Both require the `job.migrate` capability right.

If any step fails, the instance is restored on its source and the
command reports the migration as rolled back. If the rollback also
fails, the instance is running nowhere and the error says so
explicitly rather than reporting a generic failure - the two outcomes
need different responses from an operator.

Live migration needs CRIU on the destination and the capabilities the
NixOS module grants; see `services.goblin.enableMigration` in
`nix/module.nix`.

### `goblinctl cluster publish`

Publish a user event to the cluster over Serf.

```bash
goblinctl cluster publish deploy '{"version":"1.2.3"}'
```

### `goblinctl tui`

A terminal UI over the cluster and the local agents.

```bash
goblinctl tui --control-addr 10.0.0.1:31415
```

#### Overview tab

- Cluster members, with leader and follower status
- Scheduled jobs and their assignments
- Agents running on the selected node

#### Logs tab

- Cluster logs: Serf membership events, job scheduling
- Agent logs: local agent stdout and stderr
- Filtering between all, agents, and cluster

#### Controls

| Key | Action |
| --- | ------ |
| `Tab` | Switch between Overview and Logs |
| Arrow keys, or `j` / `k` | Navigate lists |
| `/` | Search |
| `f` | Filter logs |
| `q` | Quit |

## Environment variables

There is no `GOBLIN_<FLAG>` convention: daemon configuration comes from
flags only. The variables that are actually read:

| Variable | Read by | Purpose |
| -------- | ------- | ------- |
| `GOBLIN_AGENT_PATH` | agent discovery | Directories prepended to the agent search path |
| `GOBLIN_AGENT_PATH_EXCLUSIVE` | agent discovery | Search only what `GOBLIN_AGENT_PATH` names |
| `GOBLIN_VERIFY_KEY` | signature verification | Fallback for `--agent-verify-key` |
| `GOBLIN_KMSG_PATH` | PID 1 mode | Override the kmsg device |

## Quick reference

```bash
# three-node cluster
goblind start --id node-1 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.1
goblind start --id node-2 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.2 --join 10.0.0.1:31415
goblind start --id node-3 --listen-addr 0.0.0.0:31415 --advertise-addr 10.0.0.3 --join 10.0.0.1:31415

# inspect
goblinctl cluster status --control-addr 10.0.0.1:31415

# schedule
goblinctl cluster agent register ./spec.yaml
goblinctl cluster agent instances

# move a running process, memory intact
goblinctl cluster migrate-instance <instance-uuid> node-2
```
