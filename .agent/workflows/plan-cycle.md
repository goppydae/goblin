---
description: Orchestrator for the standard planning and execution lifecycle
operating_mode: audit-only | design-only
artifacts_required:
  - artifacts/intent/project_intent.md
---

# plan-cycle

## Purpose

This is a **meta-workflow** that orchestrates the standard development lifecycle defined in ADR 0001.

It does not perform work directly; it chains other workflows to ensure safety, context, and institutional memory.

## Inputs

- `auto_approve` (boolean, optional): Bypass manual plan approval. default: `false`.

## Workflow Sequence

### 1. Preparation (Audit)

1. **`prep-context`**

   - Loads `AGENTS.md`, `AGENDA.md`, and respects `.agentsignore`.
   - Produces `artifacts/logs/context_manifest.md`.

1. **`verify-agenda`**

   - Ensures `AGENDA.md` is classified and valid.
   - Prevents planning against completed or unknown items.

### 2. Planning (Design)

3. **`plan-execution`**

   - Reads context and agenda.
   - Produces `artifacts/history/runs/<run-id>/implementation_plan.md`.

   **Approval Decision**:

   - IF `auto_approve` is `true`:
     - **PROCEED** automatically to execution.
   - IF `auto_approve` is `false` (default):
     - **STOP**: Wait for operator approval of the plan.

### 3. Execution (Action)

4. **`execute-plan`**
   - Executes the approved plan.
   - Runs verification (`tools/verify_all.sh`).

### 4. Review (Audit)

5. **`post-verify`**

   - Reconciles `AGENDA.md` against reality.
   - Produces `artifacts/logs/post_verify_report.md`.

1. **`post-execution-review`**

   - Captures lessons learned in `artifacts/history/lessons-learned.md`.
   - Closes the run.
