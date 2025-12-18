---
trigger: always_on
glob:
description: Rule Number 1
---

# Rule Number 1

Before doing any substantive work (analysis, design, code edits, doc edits), you MUST run the workflow `/prep-context` and complete it.

If you have not run `/prep-context` in the current conversation/session:

- STOP and state: “Run /prep-context first.”

Exception:

- If the user explicitly asks a general conceptual question unrelated to this repo, you may answer without reading repo files, but you must say you did not consult AGENTS.md/docs.
