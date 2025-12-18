---
description: Unified finishing sequence (verify -> review -> commit)
operating_mode: audit-only
artifacts_required:
  - artifacts/intent/project_intent.md
---

# finish

## Purpose

Unified entry point for closing a development cycle. It ensures the repository is verified, history is aggregated, and a commit message is prepared.

## Workflow

1. **Verify Results**

   - Run `post-verify` to reconcile `AGENDA.md` and generate the final report.

1. **Seal History**

   - Run `post-execution-review` to aggregate the run into permanent history and extract lessons learned.

1. **Prepare Handoff**

   - Run `commit-message` to generate candidate Conventional Commit messages.

1. **Format Documentation**

   - Run `python3 tools/format_md.py` to ensure all artifacts and history files are perfectly formatted.
