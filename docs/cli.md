# Goblin CLI Reference

`goblinctl` is the unified control interface for the Goblin cluster and GAPI agents.

## TUI (Terminal User Interface)

Launch the TUI with:

```bash
goblinctl tui
```

### Features

#### Overview Tab

- **Cluster Members**: All nodes with status (Leader/Follower).
- **Cluster Jobs**: Scheduled jobs and assignments.
- **Local Agents**: Agents running on the selected node.

#### Logs Tab

- **Cluster logs**: Serf membership events, job scheduling.
- **Agent logs**: Local agent stdout/stderr.
- **Filtering**: Toggle between All/Agents/Cluster.

### Controls

- `Tab`: Switch between Overview and Logs
- `↑/↓` or `j/k`: Navigate lists
- `/`: Search
- `f`: Filter logs
- `q`: Quit

## CLI Commands

The CLI is organized into resource-based subcommands:

```text
goblinctl
├── agent             # GAPI agent management
│   ├── lifecycle     # Start/stop/restart agents
│   ├── status        # Agent status
│   └── tui           # Local agent TUI
├── job               # Scheduler operations
│   ├── run           # Submit job
│   ├── drain         # Drain node
│   └── migrate       # Migrate job
├── publish           # Serf user events
├── status            # Cluster status
└── tui               # Unified cluster + agent TUI
```

## Global Flags

| Flag         | Description                                                           |
| ------------ | --------------------------------------------------------------------- |
| `--api-addr` | Address of the Goblin node to connect to (default: `127.0.0.1:29000`) |
| `--config`   | Path to config file                                                   |
