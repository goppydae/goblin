#!/usr/bin/env python3
import re
import sys
from pathlib import Path

ALLOWED = {"software", "writing", "research", "art", "mixed", "unknown"}

def die(msg: str) -> int:
  print(f"intent_lint: ERROR: {msg}", file=sys.stderr)
  return 1

def parse_frontmatter(txt: str) -> dict[str, str]:
  # Minimal YAML frontmatter parser for key: value pairs and lists.
  m = re.match(r"(?s)\A---\s*\n(.*?)\n---\s*\n", txt)
  if not m:
    return {}
  block = m.group(1)
  out: dict[str, str] = {}
  current_key = None
  for line in block.splitlines():
    stripped = line.strip()
    if not stripped or stripped.startswith("#"):
      continue
    # Check if it's a list item (starts with -)
    if stripped.startswith("-") and current_key:
      # Part of a list for current_key
      if current_key not in out:
        out[current_key] = stripped
      else:
        out[current_key] += " " + stripped
      continue
    if ":" not in line:
      continue
    k, v = line.split(":", 1)
    k = k.strip()
    v = v.strip().strip('"').strip("'")
    out[k] = v
    current_key = k if v in ("|", ">", "") else None
  return out

def main() -> int:
  p = Path("artifacts/intent/project_intent.md")
  if not p.exists():
    return die("missing artifacts/intent/project_intent.md (run establish-intent first)")

  txt = p.read_text(encoding="utf-8")
  fm = parse_frontmatter(txt)
  if not fm:
    return die("project_intent.md missing YAML frontmatter (--- ... ---)")

  required = ["primary_domain", "deliverable", "first_milestone_done", "constraints", "non_goals"]
  missing = [k for k in required if k not in fm or fm[k] == ""]
  if missing:
    return die("project_intent.md missing required keys: " + ", ".join(missing))

  dom = fm["primary_domain"]
  if dom not in ALLOWED:
    return die(f"primary_domain must be one of {sorted(ALLOWED)}, got '{dom}'")

  print("intent_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
