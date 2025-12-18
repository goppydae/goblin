#!/usr/bin/env python3
import sys
import json
import datetime
from pathlib import Path
from typing import Optional

from journal import emit_journal

def die(msg: str) -> int:
    print(f"close_run: ERROR: {msg}", file=sys.stderr)
    return 1

def note(msg: str):
    print(f"close_run: {msg}")

def get_latest_run() -> Optional[Path]:
    runs_dir = Path("artifacts/history/runs")
    if not runs_dir.exists():
        return None
    runs = sorted([d for d in runs_dir.iterdir() if d.is_dir()])
    return runs[-1] if runs else None

def extract_lessons(run_dir: Path) -> list:
    wt = run_dir / "walkthrough.md"
    if not wt.exists():
        return []
    
    content = wt.read_text(encoding="utf-8")
    # Looking for a "Lessons Learned" section
    # Simple extraction: all bullets under a header that contains "Lessons"
    lines = content.splitlines()
    lessons = []
    in_lessons = False
    
    for line in lines:
        if line.startswith("#") and "lessons" in line.lower():
            in_lessons = True
            continue
        if in_lessons and line.startswith("#"):
            in_lessons = False
            break
        if in_lessons and line.strip().startswith("- "):
            lessons.append(line.strip()[2:])
            
    return lessons

def update_global_lessons(lessons: list, run_name: str) -> int:
    """Append unique lessons to artifacts/history/lessons-learned.md.
    """
    if not lessons:
        return 0
        
    global_file = Path("artifacts/history/lessons-learned.md")
    
    # Ensure header
    if not global_file.exists():
        global_file.write_text("# Lessons Learned\n\n", encoding="utf-8")
        
    content = global_file.read_text(encoding="utf-8")
    
    new_entries = [f"\n## {run_name}\n"]
    for idx, lesson in enumerate(lessons, start=1):
        # Clean up lesson text
        clean_lesson = lesson.strip()
        if clean_lesson.endswith("."): clean_lesson = clean_lesson[:-1]
        
        # Simple title extraction (first 5 words)
        title = " ".join(clean_lesson.split()[:5]) + "..."
        
        new_entries.append(f"\n### {idx}. {title}\n")
        new_entries.append(f"\n**Lesson**: {clean_lesson}.\n")
        new_entries.append(f"\n**Evidence**: from [{run_name}](runs/{run_name}/walkthrough.md)\n")
        
    if len(new_entries) > 1:
        # Ensure the file ends with a newline before appending
        existing_content = global_file.read_text(encoding="utf-8")
        if existing_content and not existing_content.endswith("\n\n"):
            with open(global_file, "a", encoding="utf-8") as f:
                f.write("\n")
                
        with open(global_file, "a", encoding="utf-8") as f:
            f.writelines(new_entries)
        note(f"Added {len(lessons)} lessons to {global_file} under header {run_name}")
    else:
        note(f"No new lessons to add to {global_file}")

    return len(new_entries)

def main() -> int:
    if len(sys.argv) > 1:
        run_name = sys.argv[1]
        run_dir = Path("artifacts/history/runs") / run_name
    else:
        run_dir = get_latest_run()
        if not run_dir:
            return die("no runs found")
        run_name = run_dir.name

    if not run_dir.exists():
        return die(f"run directory {run_dir} does not exist")

    note(f"Closing run: {run_name}")

    # 1. Verify run integrity (basic check, rely on verify_all for details)
    if not (run_dir / "implementation_plan.md").exists():
        return die("missing implementation_plan.md")

    note("Generating journal artifact for the run…")
    journal_path = emit_journal(run_dir)
    if not journal_path:
        note("journal generation failed or no artifacts found")

    if journal_path:
        note(f"Journal written to {journal_path}")
    else:
        note("No journal entries produced (missing recognized artifacts)")
    
    # 2. Extract and promote lessons
    lessons = extract_lessons(run_dir)
    added_lessons = 0
    if lessons:
        added_lessons = update_global_lessons(lessons, run_name)
    else:
        note("No lessons extraction found in walkthrough.md (section 'Lessons Learned')")

    # 3. Seal the run
    closure = {
        "closed_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "final_status": "closed",
        "lessons_extracted": len(lessons),
        "lessons_added": added_lessons
    }
    
    with open(run_dir / "closure.json", "w") as f:
        json.dump(closure, f, indent=2)
        
    note(f"Run {run_name} closed successfully.")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
