---
description: Re-run agenda verification after execution and record reconciliation results
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
  - artifacts/logs/post_verify_report.md
---

# post-verify

Precondition:
- `artifacts/intent/project_intent.md` exists.

If precondition is not met:
- fail closed (panic) and immediately initiate `establish-intent` by asking:
  - "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"
- write `artifacts/intent/project_intent.md`
- then continue with the requested workflow

After executing a plan, re-verify that `AGENDA.md` reflects reality.

## Output requirements

Emit `artifacts/logs/post_verify_report.md` with the following **exact** section headings:

- `## Completed items`
- `## Items still open`
- `## Evidence`

Rules:
- Evidence pointers MUST be repo-relative paths (e.g. `artifacts/test_results/...`).
- Evidence pointers MUST NOT be absolute paths and MUST NOT use `file://` URLs.
- Avoid truncation (`...`) in evidence pointers; they must be auditable.
