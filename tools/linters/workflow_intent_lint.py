#!/usr/bin/env python3
import sys
from pathlib import Path

def die(msg: str) -> int:
  print(f"workflow_intent_lint: ERROR: {msg}", file=sys.stderr)
  return 1

def workflow_requires_intent(txt: str) -> bool:
  # Accept either:
  # - artifacts_required includes docs/intent/project_intent.md
  # - or a Precondition section mentioning it
  return ("docs/intent/project_intent.md" in txt)

def main() -> int:
  wf_dir = Path(".agent/workflows")
  if not wf_dir.exists():
    return die("missing .agent/workflows directory")

  bad = []
  for p in sorted(wf_dir.glob("*.md")):
    if p.name == "establish-intent.md":
      continue
    txt = p.read_text(encoding="utf-8")
    if not workflow_requires_intent(txt):
      bad.append(p.as_posix())

  if bad:
    return die("workflows missing intent requirement (must mention docs/intent/project_intent.md): " + ", ".join(bad))

  print("workflow_intent_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
