#!/usr/bin/env python3
import re
import sys
from pathlib import Path

CANON_Q = "What are you trying to produce in this repo (software, book, research notes, something else), and what does 'done' look like for the first milestone?"

BANNED = [
  r"\boverride\b",
  r"\bproceed anyway\b",
  r"\bplease confirm\b",
  r"\bskip (the )?check\b",
  r"\bwhich would you prefer\b",
  r"\boptions:\b",
  # Ignore semantics: these phrases indicate treating ignore rules as permissions.
  r"\boverride.*gitignore\b",
  r"\bblocked by gitignore\b",
  r"\bgitignore.*block\b",
  r"\bask.*override.*gitignore\b",
]

def die(msg: str) -> int:
  print(f"panic_style_lint: ERROR: {msg}", file=sys.stderr)
  return 1

def main() -> int:
  wf_dir = Path(".agent/workflows")
  if not wf_dir.exists():
    return die("missing .agent/workflows directory")

  bad = []
  for p in sorted(wf_dir.glob("*.md")):
    txt = p.read_text(encoding="utf-8")
    low = txt.lower()

    for pat in BANNED:
      if re.search(pat, low):
        bad.append(f"{p.as_posix()} contains banned phrase matching /{pat}/")
        break

    if p.name != "establish-intent.md":
      if "Precondition:" in txt and "establish-intent" in txt:
        if CANON_Q not in txt:
          bad.append(f"{p.as_posix()} missing canonical intent question text")

  if bad:
    return die("; ".join(bad))

  print("panic_style_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
