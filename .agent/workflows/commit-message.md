---
description: Generate a Conventional Commit message from changes since the last commit
operating_mode: audit-only | design-only
artifacts_required:
  - artifacts/intent/project_intent.md
---

# commit-message

Precondition:
- `artifacts/intent/project_intent.md` exists.

If precondition is not met:
- fail closed (panic) and immediately initiate `establish-intent` by asking:
  - "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"
- write `artifacts/intent/project_intent.md`
- then continue with this workflow

## Commit message format (normative)

Use **Conventional Commits**:

```
<type>(<scope>): <short summary>

<body>   # optional: why + what changed
<footer> # optional: Fixes #123, BREAKING CHANGE: ...
```

Allowed `type` values (preferred): `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `chore`, `build`, `ci`.

Rules:
- Summary line is imperative mood, <= ~72 chars, no trailing period.
- `scope` is optional; use a short subsystem name if it clarifies impact.
- If change is breaking, include `!` after type/scope (e.g., `feat(api)!:`) and add `BREAKING CHANGE:` in footer.

## Link hygiene (normative)

Commit messages must be portable across machines, editors, and hosting platforms.

### Allowed references
- **Repo-relative file paths**:
  - Prefer inline code: `tools/linters/rules.json`
  - If you really want a clickable link, it must be repo-relative (no scheme):
    - `[tools/linters/rules.json](tools/linters/rules.json)`
- **Public web links** when the target is truly external:
  - `https://…` only

### Forbidden references (must not appear)
- Any host filesystem links or editor/IDE deep-links, including (non-exhaustive):
  - `file://…`
  - `cci:…`
  - `vscode:…`, `idea:…`
  - Absolute local paths like `/home/...`, `/Users/...`, `C:\...`

### Sanitization rule
If a generated commit message includes forbidden links:
- replace them with **repo-relative paths in backticks**, or
- replace with a **repo-relative markdown link**, or
- remove the link entirely if it adds no value.

## Authority and safety (normative)

- The agent MUST NOT stage, commit, amend commits, push, rebase, or otherwise mutate Git history.
- The agent’s output is limited to:
  - commit message candidates,
  - notes about what changed and why,
  - link sanitization and formatting fixes.

Rationale:
- Staging/committing is operator-controlled. Repo policy may also enforce this elsewhere (e.g., AGENTS.md),
  but this workflow independently enforces it to prevent drift.

## Steps

1. **Review diff since last commit**
   - Read and summarize the workspace diff vs `HEAD`.
   - Identify: user-facing behavior changes, refactors, tests/docs, build/CI, and any breaking changes.

2. **Draft message content**
   - Write the header first (type/scope/summary).
   - Add a body only when it increases clarity (why + what changed).
   - If multiple concerns exist, choose the dominant one and mention secondary changes in the body.

3. **Propose message candidates**
   - Produce 2–3 candidate commit messages (header + optional body/footer) matching the format above.
   - Prefer the most semantically accurate `type`.

4. **Sanitize references**
   - Ensure any file mentions are **repo-relative** and not auto-linked to local paths.
   - Hard-check the message for forbidden patterns:
     - `"://"` (except `https://`)
     - `cci:`
     - `file://`
     - `/home/`, `/Users/`, `C:\`
   - Fix any violations using the Sanitization rule above.

5. **Approval gate**
   - Present the top candidate as the default.
   - Ask the user to approve or edit the message.

6. **Stop**
   - Do not run any Git commands.
   - The operator performs staging and committing.
