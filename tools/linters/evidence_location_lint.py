#!/usr/bin/env python3
import sys
from pathlib import Path
from lint_common import die


def main() -> int:
  bad = []
  tr = Path("artifacts/test_results")
  if tr.exists():
    for p in tr.glob("*lint*.log"):
      if p.is_file():
        bad.append(p.as_posix())

  if bad:
    return die("evidence_location_lint", "lint logs must be in artifacts/logs, but found under artifacts/test_results: " + ", ".join(bad))


  print("evidence_location_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
