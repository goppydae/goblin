#!/usr/bin/env python3
"""Deep Thoughts - Anthropomorphic Journal Generator.

This tool reconstructs a narrative "journal entry" from a run's artifacts.
It is deterministic, post-hoc, and theatrical.
"""

import json
import re
import sys
from pathlib import Path
from typing import Dict, List, Optional

HEADER = "### Deep Thoughts, by an Agent"
DISCLAIMER = "*Editor’s note: This entry is a dramatized reconstruction of a deterministic decision process, derived from run artifacts.*"


def die(msg: str) -> int:
    print(f"journal: ERROR: {msg}", file=sys.stderr)
    return 1


def note(msg: str):
    print(f"journal: {msg}")


def load_plan_summary(run_dir: Path) -> str:
    plan_path = run_dir / "implementation_plan.json"
    if not plan_path.exists():
        return "I had no plan, behaving purely reactively."
    
    try:
        data = json.loads(plan_path.read_text(encoding="utf-8"))
        items = data.get("items", [])
        if not items:
            return "I had an empty plan."
        
        count = len(items)
        first_hyp = items[0].get("hypothesis", "something unknown")
        return f"I set out to test {count} hypotheses, starting with '{first_hyp}'."
    except Exception:
        return "I had a plan, but it was indecipherable."


def extract_lessons(walkthrough_path: Path) -> List[str]:
    if not walkthrough_path.exists():
        return []
    
    content = walkthrough_path.read_text(encoding="utf-8")
    lines = content.splitlines()
    lessons: List[str] = []
    in_lessons = False

    for line in lines:
        if line.startswith("#") and "lessons" in line.lower():
            in_lessons = True
            continue
        if in_lessons and line.startswith("#"):
            break
        if in_lessons and line.strip().startswith("- "):
            lessons.append(line.strip()[2:])

    return lessons


def load_outcome(run_dir: Path) -> str:
    report_path = run_dir / "post_verify_report.md"
    if not report_path.exists():
        return "The run concluded without a final report."
    
    text = report_path.read_text(encoding="utf-8")
    status_match = re.search(r"Status:\s*([A-Za-z\-]+)", text)
    status = status_match.group(1).strip() if status_match else "unknown"
    
    return f"The run finished with status '{status}'."


def generate_narrative(run_id: str, plan_summary: str, outcome: str, lessons: List[str]) -> str:
    """Constructs the deterministic narrative."""
    
    narrative = [
        f"**Run {run_id}**",
        "",
        f"**Goal**: {plan_summary}",
        f"**Outcome**: {outcome}",
        "",
        "**Reflections**:",
    ]
    
    if lessons:
        for lesson in lessons:
            narrative.append(f"- {lesson}")
    else:
        narrative.append("- I learned nothing specific this time.")
        
    narrative.append("")
    narrative.append("**Decision**: I proceeded with the available evidence.")
    
    return "\n".join(narrative)


def emit_journal(run_dir: Path) -> Optional[Path]:
    run_id = run_dir.name
    
    # 1. Gather context
    plan_summary = load_plan_summary(run_dir)
    outcome = load_outcome(run_dir)
    lessons = extract_lessons(run_dir / "walkthrough.md")
    
    # 2. Generate content
    body = generate_narrative(run_id, plan_summary, outcome, lessons)
    
    # 3. Format artifact
    content = f"{HEADER}\n*(reconstructed)*\n\n{body}\n\n---\n\n{DISCLAIMER}\n"
    
    # 4. Write
    journal_dir = Path("artifacts/journal")
    journal_dir.mkdir(parents=True, exist_ok=True)
    
    out_path = journal_dir / f"{run_id}.md"
    try:
        out_path.write_text(content, encoding="utf-8")
    except OSError as exc:
        die(f"failed to write {out_path}: {exc}")
        return None

    note(f"wrote journal to {out_path}")
    return out_path


def get_latest_run() -> Optional[Path]:
    runs_dir = Path("docs/exec/runs")
    if not runs_dir.exists():
        return None
    # Sort by name (timestamp)
    runs = sorted([d for d in runs_dir.iterdir() if d.is_dir()])
    return runs[-1] if runs else None


def main(argv: Optional[List[str]] = None) -> int:
    if argv is None:
        argv = sys.argv[1:]
    if argv:
        run_dir = Path(argv[0])
    else:
        run_dir = get_latest_run()
        if not run_dir:
            return die("no runs found")

    if not run_dir.exists():
        return die(f"run directory {run_dir} does not exist")

    emit_journal(run_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
