#!/usr/bin/env python3
import json
import sys
from pathlib import Path
from lint_common import die, find_run_artifact

def load(path: Path) -> dict:
  try:
    return json.loads(path.read_text(encoding="utf-8"))
  except FileNotFoundError:
    raise
  except Exception as e:
    raise ValueError(str(e))

def lint_obj(obj: object) -> None:
  if not isinstance(obj, dict) or "meta" not in obj or "items" not in obj:
    raise ValueError("plan missing meta/items")
  if not isinstance(obj["items"], list) or not obj["items"]:
    raise ValueError("plan items must be non-empty array")


def main(argv: list[str]) -> int:
  mode_run = False
  if len(argv) >= 2 and argv[1] == "--run":
    mode_run = True

  if mode_run:
    p = find_run_artifact("implementation_plan.json")
    if p is None:
      return die("plan_lint", "no docs/exec/runs/**/implementation_plan.json found")
  else:
    p = Path("implementation_plan.json")
    if not p.exists():
      return die("plan_lint", "implementation_plan.json not found")

  try:
    obj = load(p)
    lint_obj(obj)
  except FileNotFoundError:
    return die("plan_lint", f"{p.as_posix()} not found")
  except ValueError as e:
    return die("plan_lint", f"{p.as_posix()}: {e}")

  print(f"plan_lint: OK ({p.as_posix()})")
  return 0

if __name__ == "__main__":
  raise SystemExit(main(sys.argv))
