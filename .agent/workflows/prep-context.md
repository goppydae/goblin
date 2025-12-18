---
description: Load and verify workspace context
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
  - artifacts/logs/context_manifest.md
---

# prep-context

Precondition:

- `artifacts/intent/project_intent.md` exists.

If precondition is not met:

- fail closed (panic) and immediately initiate `establish-intent` by asking:
  - "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"
- write `artifacts/intent/project_intent.md`
- then continue with the requested workflow

## Purpose

Load required context before planning:

- `AGENTS.md`
- `AGENDA.md`
- relevant `docs/` (if present)

## Agent Ignore

MUST respect `.agentsignore` when selecting any additional files to read.
If `.agentsignore` is missing or malformed, fail closed.

## Output: context_manifest.md

Emit `artifacts/logs/context_manifest.md` containing:

- timestamp (ISO-8601)
- operating mode
- agent ignore file used (`.agentsignore`)
- files read as context (repo-relative paths)
- files skipped due to `.agentsignore` (repo-relative paths, if detectable)
- any read-scope budget applied (count/bytes) and whether it truncated

**Generation**: Run `python3 tools/generate_context_manifest.py` to create this file.

No code/config modifications are permitted in this workflow.
