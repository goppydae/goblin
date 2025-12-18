---
description: Begin a new session or work cycle
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
---

# start

## Purpose

User-friendly entry point for the agentic development cycle.

It ensures basics are in place (`project_intent.md`) and then hands off to the main orchestrator (`plan-cycle`).

## Inputs

- `auto_approve` (boolean, optional): If true, skips the plan approval gate. default: `false`.

> [!WARNING]
> Setting `auto_approve: true` removes the human-in-the-loop safety check. Use only for routine tasks or trusted automated loops.

## Workflow

1. **Check Intent**

   - Check if `artifacts/intent/project_intent.md` exists.
   - If MISSING, run `establish-intent`.

1. **Hand off to Cycle**

   - Run `plan-cycle` workflow with `auto_approve` argument.
