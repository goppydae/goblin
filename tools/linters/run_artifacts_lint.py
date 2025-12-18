#!/usr/bin/env python3
import sys
from pathlib import Path
from lint_common import die, find_run_artifact


def main() -> int:
  # Root hygiene checks
  bad = []
  for name in ["implementation_plan.md", "implementation_plan.json", "walkthrough.md"]:
    if Path(name).exists():
      bad.append(name)
  # Any *.resolved* or *.metadata.json at root
  for p in Path(".").glob("*.resolved*"):
    bad.append(p.name)
  for p in Path(".").glob("*.metadata.json"):
    bad.append(p.name)

  if bad:
    return die("run_artifacts_lint", "root contains forbidden execution artifacts: " + ", ".join(sorted(set(bad))))

  # Check if there are any run directories
  runs_dir = Path("artifacts/history/runs")
  has_runs = False
  if runs_dir.exists():
    for child in runs_dir.iterdir():
      if child.is_dir():
        has_runs = True
        break
  
  if not has_runs:
    # No runs verify, so we are good (hygiene already checked)
    print("run_artifacts_lint: OK (no runs)")
    return 0

  # Require at least one run folder with these artifacts if runs exist.
  plan = find_run_artifact("implementation_plan.json")
  w = find_run_artifact("walkthrough.md")
  if plan is None:
    return die("run_artifacts_lint", "runs exist but no artifacts/history/runs/**/implementation_plan.json found")
  if w is None:
    return die("run_artifacts_lint", "runs exist but no artifacts/history/runs/**/walkthrough.md found")



  print("run_artifacts_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
