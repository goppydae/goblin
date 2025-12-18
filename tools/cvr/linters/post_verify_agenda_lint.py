#!/usr/bin/env python3
import re
import sys
from pathlib import Path
from lint_common import die


def main() -> int:
  report_p = Path("artifacts/logs/post_verify_report.md")
  agenda_p = Path("AGENDA.md")
  if not report_p.exists():
    return die("post_verify_agenda_lint", "missing artifacts/logs/post_verify_report.md")
  if not agenda_p.exists():
    return die("post_verify_agenda_lint", "missing AGENDA.md")

  report = report_p.read_text(encoding="utf-8")
  agenda = agenda_p.read_text(encoding="utf-8")

  # Extract hypothesis id from report (Run ID line is common, also headings).
  m = re.search(r"Run ID:\s*([0-9]{4}-[0-9]{2}-[0-9]{2}_[A-Z]+-[0-9]{4,})", report)
  hyp = None
  if m:
    hyp = m.group(1).split("_", 1)[1]
  else:
    m2 = re.search(r"\b(HYP-[0-9]{4,})\b", report)
    if m2:
      hyp = m2.group(1)

  if hyp is None:
    return die("post_verify_agenda_lint", "could not find hypothesis id (HYP-####) in post_verify_report.md")

  # Determine whether report claims nothing open.
  open_section = re.search(r"## Items still open\s*(.*?)(?:\n## |\Z)", report, flags=re.DOTALL)
  if open_section is None:
    return die("post_verify_agenda_lint", "post_verify_report.md missing '## Items still open' section")

  open_body = open_section.group(1).strip().lower()
  report_says_none_open = open_body.startswith("none")

  # Find agenda block for that hyp id
  # Expect a block containing "ID: HYP-####" and "Status:"
  block_re = re.compile(rf"(?ms)^\s*-\s*\[\s*\]\s*ID:\s*{re.escape(hyp)}.*?(?=^\s*-\s*\[\s*\]\s*ID:|\Z)")
  bm = block_re.search(agenda)
  if bm is None:
    return die("post_verify_agenda_lint", f"AGENDA.md missing item with ID: {hyp}")

  block = bm.group(0)
  sm = re.search(r"^\s*Status:\s*([A-Za-z\-]+)\s*$", block, flags=re.MULTILINE)
  if sm is None:
    return die("post_verify_agenda_lint", f"AGENDA.md item {hyp} missing Status:")
  status = sm.group(1).strip()

  if report_says_none_open and status != "finished":
    return die("post_verify_agenda_lint", f"post-verify says no open items, but AGENDA status for {hyp} is '{status}' (expected 'finished')")

  if (not report_says_none_open) and status == "finished":
    return die("post_verify_agenda_lint", f"post-verify indicates open work, but AGENDA status for {hyp} is 'finished'")

  print(f"post_verify_agenda_lint: OK ({hyp}, status={status})")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
