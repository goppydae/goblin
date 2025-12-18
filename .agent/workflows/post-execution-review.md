---
description: Capture institutional memory from an executed plan
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
  - artifacts/history/lessons-learned.md
---

# post-execution-review

Precondition:

- `artifacts/intent/project_intent.md` exists.

If precondition is not met:

- fail closed (panic) and immediately initiate `establish-intent` by asking:
  - "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"
- write `artifacts/intent/project_intent.md`
- then continue with the requested workflow

Inputs:

- `artifacts/history/runs/<run-id>/walkthrough.md`
- `artifacts/logs/post_verify_report.md` (preferred)
- Evidence under `artifacts/`

Rules:

- Entries MUST include evidence pointers (repo-relative paths).
- Do NOT add an entry if there is no evidence.
