---
title: "goblinctl agent"
---

## goblinctl agent

Local agent management operations

### Options

```
  -h, --help   help for agent
```

### Options inherited from parent commands

```
      --control-addr string   Address of the target daemon's control plane (empty: from config)
      --log-level string      Log level: debug, info, warn, error
      --tls-ca string         CA certificate used to verify the daemon
      --tls-insecure          Skip verification (INSECURE)
  -v, --version               version for goblinctl
```

### SEE ALSO

* [goblinctl](./goblinctl/)	 - Goblin distributed supervisor control
* [goblinctl agent build](./goblinctl_agent_build/)	 - Build Go agents
* [goblinctl agent clean](./goblinctl_agent_clean/)	 - Clean build artifacts
* [goblinctl agent crypto](./goblinctl_agent_crypto/)	 - Cryptography utilities
* [goblinctl agent lifecycle](./goblinctl_agent_lifecycle/)	 - Control agent lifecycle
* [goblinctl agent new](./goblinctl_agent_new/)	 - Create a new agent from template
* [goblinctl agent ping](./goblinctl_agent_ping/)	 - Ping the daemon
* [goblinctl agent reload](./goblinctl_agent_reload/)	 - Trigger a reload of registered agents
* [goblinctl agent shutdown](./goblinctl_agent_shutdown/)	 - Request a system shutdown from the daemon (poweroff by default)
* [goblinctl agent status](./goblinctl_agent_status/)	 - Show current registered agents
* [goblinctl agent tui](./goblinctl_agent_tui/)	 - Local Agent TUI
* [goblinctl agent verify](./goblinctl_agent_verify/)	 - Verify agent binary integrity and authenticity

