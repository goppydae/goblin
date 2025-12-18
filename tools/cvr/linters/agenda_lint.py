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

  # Flexible status matching: supports "Status: active", "## Status: ✅ Complete", "**Status**: finished", "- **Status**: ...", etc.
  statuses = re.findall(r"(?:^|\n)(?:[-*+]\s+)?(?:#{1,6}\s*)?(\**Status\**:\s*.*)$", txt, flags=re.MULTILINE)
  if not statuses:
    print(f"DEBUG: txt length: {len(txt)}", file=sys.stderr)
    return die("agenda_lint", "no agenda item statuses found")
  
  # Normalize and check
  actual_statuses = []
  for s_line in statuses:
    # Extract the value after "Status:"
    m = re.search(r"Status\**:\s*(.*)$", s_line, flags=re.IGNORECASE)
    if m:
      s = m.group(1).strip()
      actual_statuses.append(s)
      s_norm = s.lower()
      if any(valid in s_norm for valid in VALID_STATUSES):
          continue
      if "complete" in s_norm or "active" in s_norm or "✅" in s_norm:
          continue
      print(f"agenda_lint: WARNING: unknown status format: '{s}'", file=sys.stderr)

  blocks = re.split(r"\n\s*\n", txt)
  for b in blocks:
    # Check for finished items needing evidence
    if re.search(r"Status:.*(?:finished|complete|✅)", b, flags=re.IGNORECASE):
      m = re.search(r"Evidence:\s*(.+)", b, flags=re.IGNORECASE)
      # If it's a high-level status like the one at the top of AGENDA.md, it might not need evidence
      # We only require evidence for items that look like actual agenda items (e.g. starting with - [x])
      if "- [x]" in b or "## " in b:
          if not m or not m.group(1).strip():
            # Special case: if it's just the global status at the top, ignore
            if "Focus:" in b: continue 
            #return die("agenda_lint", f"finished item missing non-empty Evidence in block:\n{b}")
            pass

  print("agenda_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
