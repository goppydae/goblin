#!/usr/bin/env python3
import re
import sys
from pathlib import Path
from lint_common import die, FILE_URL_RE, ABS_PATH_RE, TRUNC_RE, find_run_artifact

def find_walkthrough() -> Path | None:
  root = Path("walkthrough.md")
  if root.exists():
    return root
  return find_run_artifact("walkthrough.md")


def main() -> int:
  p = find_walkthrough()
  if p is None:
    return die("walkthrough_lint", "no walkthrough.md found (expected artifacts/history/runs/**/walkthrough.md)")

  # Root walkthrough is forbidden by v5.3, but we keep a specific message here.
  if p.resolve().name == "walkthrough.md" and p.parent.resolve() == Path(".").resolve():
    return die("walkthrough_lint", "walkthrough.md is at repo root; must be under artifacts/history/runs/<run-id>/")

  txt = p.read_text(encoding="utf-8")

  if FILE_URL_RE.search(txt):
    return die("walkthrough_lint", "walkthrough contains file:// URLs; use repo-relative paths only")
  if ABS_PATH_RE.search(txt):
    return die("walkthrough_lint", "walkthrough contains an absolute path; use repo-relative paths only")
  if TRUNC_RE.search(txt):
    return die("walkthrough_lint", "walkthrough contains '...'; do not truncate evidence pointers")
  if re.search(r"Artifacts\s*\(Brain\)", txt, flags=re.IGNORECASE):
    return die("walkthrough_lint", "walkthrough contains 'Artifacts (Brain)'; workspace artifacts only")

  print(f"walkthrough_lint: OK ({p.as_posix()})")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
