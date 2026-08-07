---
title: "goblind"
---

## goblind

Goblin Distributed Supervisor Daemon

### Options

```
  -h, --help                  help for goblind
      --id string             Unique node identifier (default: hostname)
      --log-file string       Additional rotated file sink
      --log-format string     Log format: json or console
      --log-level string      Log level: debug, info, warn, error
      --log-loki-url string   Forward logs to a Loki endpoint
      --metrics-addr string   Prometheus listen address (empty: disabled)
      --tls-ca string         CA certificate for peer verification
      --tls-cert string       This node's certificate
      --tls-key string        This node's private key
  -v, --version               version for goblind
```

### SEE ALSO

* [goblind start](./goblind_start/)	 - Start the goblind daemon
* [goblind version](./goblind_version/)	 - Print version info

