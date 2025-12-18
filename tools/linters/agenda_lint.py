#!/usr/bin/env python3
import re
import sys
from pathlib import Path
from lint_common import die

REQUIRED_HEADINGS = ["## Active Hypotheses", "## Blockers", "## Deferred Risks"]
VALID_STATUSES = {"finished", "in-progress", "blocked", "not-started", "unknown"}

def main() -> int:
  p = Path("AGENDA.md")
  if not p.exists():
    return die("agenda_lint", "AGENDA.md not found")
  txt = p.read_text(encoding="utf-8")

  for h in REQUIRED_HEADINGS:
    if h not in txt:
      return die("agenda_lint", f"missing required heading: {h}")

  statuses = re.findall(r"^\s*Status:\s*([A-Za-z\-]+)\s*$", txt, flags=re.MULTILINE)
  if not statuses:
    return die("agenda_lint", "no agenda item statuses found")
  bad = sorted({s for s in statuses if s not in VALID_STATUSES})
  if bad:
    return die("agenda_lint", f"invalid status values: {', '.join(bad)}")

  blocks = re.split(r"\n\s*\n", txt)
  for b in blocks:
    if re.search(r"^\s*Status:\s*finished\s*$", b, flags=re.MULTILINE):
      m = re.search(r"^\s*Evidence:\s*(.+)\s*$", b, flags=re.MULTILINE)
      if not m or not m.group(1).strip():
        return die("agenda_lint", "finished item missing non-empty Evidence:")

  print("agenda_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
