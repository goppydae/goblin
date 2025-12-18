---
description: Toggle MAINTENANCE mode for the agent (operator-controlled)
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
  - artifacts/logs/agent_mode.json
---

# toggle-maintenance-mode

## Purpose

Operator-controlled switch that marks the agent as being in **MAINTENANCE mode** (or not).

This is a **state toggle only**. It does not stage/commit/push anything, and it does not modify Runtime code by itself.

## Inputs

- `state` (string, optional): one of `on`, `off`, `toggle`. default: `toggle`.
- `reason` (string, optional): short human note explaining why maintenance is being enabled.

## Normative behavior

### State file

Maintain exactly one state file:

- `artifacts/logs/agent_mode.json`

Schema:

```json
{
  "mode": "maintenance" | "normal",
  "timestamp": "ISO-8601",
  "reason": "string (optional)"
}
```

### Transition rules

- If `state: on`:
  - write the state file with `"mode": "maintenance"`.
- If `state: off`:
  - write the state file with `"mode": "normal"` (do not delete the file; keep an audit trail).
- If `state: toggle`:
  - if current mode is `maintenance`, switch to `normal`.
  - otherwise switch to `maintenance`.

### Continuous notification requirement

While `"mode": "maintenance"` is active, the agent MUST prepend the following banner to **every** response until the mode is set back to `normal`:

> 🛠️ **MAINTENANCE MODE ENABLED** — Runtime modifications may be in progress. Operator intent required for any risky actions.

The banner is required even in audit-only replies.

### Safety note (normative)

- MAINTENANCE mode only expands what changes are *permitted* under `AGENTS.md`; it does not remove any other constraints.
- The agent MUST still:
  - fail closed when preconditions are unmet
  - produce evidence and follow the scientific method for non-trivial changes
  - avoid staging/committing/pushing (operator-only)

## Steps

1. Read `artifacts/intent/project_intent.md` (precondition consistency check).
2. Read current `artifacts/logs/agent_mode.json` if it exists; otherwise assume `"normal"`.
3. Apply Transition rules, writing the updated state file.
4. Echo the new mode + timestamp + reason (if provided) to the operator.
5. Stop.
