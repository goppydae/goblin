---
description: Produce implementation plan artifacts from verified agenda
operating_mode: design-only
artifacts_required:
  - artifacts/intent/project_intent.md
  - artifacts/history/runs/<run-id>/implementation_plan.md
  - artifacts/history/runs/<run-id>/implementation_plan.json
---

# plan-execution

Precondition:

- `artifacts/intent/project_intent.md` exists and reflects the repo's purpose.

If precondition is not met:

- fail closed (panic) and immediately initiate `establish-intent` by asking:
  - "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"
- write `artifacts/intent/project_intent.md`
- then continue with the requested workflow

## Prerequisites (conditional)

Before drafting any plan, ensure context is loaded and agenda is verified:

1. **Context Manifest**:

   - Check if `artifacts/logs/context_manifest.md` exists.
   - If MISSING, run `prep-context` workflow.

1. **Agenda Verification**:

   - Check if `artifacts/logs/post_verify_report.md` exists and is fresher than the latest `AGENDA.md` edit (heuristic).
   - If in doubt, or if never run for this session, run `verify-agenda` workflow.

## Ignore semantics (normative)

`.gitignore` and `.agentsignore` are NOT permission systems.
They MUST NOT be treated as a precondition failure or a panic.

- You MUST write planning artifacts under `artifacts/history/runs/<run-id>/` even if git ignores that directory.
- Do NOT ask the user to bypass ignore rules.
- Ignore rules only affect what gets committed or included in agent context, not what can be created on disk.

## Context load (normative)

Before drafting any plan, read:

- `artifacts/intent/project_intent.md`
- `AGENTS.md`
- `AGENDA.md`

Use the intent to choose domain-appropriate planning:

- software: design + tests + build verification
- writing: outlines + milestones + editorial workflow
- research: hypotheses + methods + citations + reproducibility steps
- art: briefs + iterations + asset workflow
- mixed/unknown: minimal, conservative plan with explicit unknowns

## Run directory

All plan artifacts MUST be written under:

`artifacts/history/runs/<run-id>/`

## Outputs (normative paths)

- `artifacts/history/runs/<run-id>/implementation_plan.md`
- `artifacts/history/runs/<run-id>/implementation_plan.json`

Workspace root MUST NOT be used for plan artifacts.
