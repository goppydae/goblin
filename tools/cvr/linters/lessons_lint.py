#!/usr/bin/env python3
import re
from pathlib import Path
from lint_common import die, ABS_PATH_RE, TRUNC_RE

# Match actual file:// URLs (file:// followed by non-whitespace/non-backtick chars)
FILE_URL_PATTERN = re.compile(r"file://[^\s`]+")


def strip_backtick_content(text: str) -> str:
  """Remove content within backticks to avoid false positives from examples."""
  # Remove inline code (single backticks)
  text = re.sub(r"`[^`]*`", "", text)
  # Remove code blocks (triple backticks)
  text = re.sub(r"```[^`]*```", "", text, flags=re.DOTALL)
  return text


def main() -> int:
  p = Path("artifacts/history/lessons-learned.md")
  if not p.exists():
    return die("lessons_lint", "artifacts/history/lessons-learned.md missing")

  txt = p.read_text(encoding="utf-8")

  if "# Lessons Learned" not in txt:
    return die("lessons_lint", "missing title")

  if "- Evidence:" not in txt:
    return die("lessons_lint", "missing Evidence field in template or entries")

  if FILE_URL_PATTERN.search(txt):
    return die("lessons_lint", "lessons-learned.md contains file://; use repo-relative paths only")

  # Strip backtick content before checking for patterns that might appear in documentation
  txt_no_code = strip_backtick_content(txt)

  if ABS_PATH_RE.search(txt_no_code):
    return die("lessons_lint", "lessons-learned.md appears to contain an absolute path; use repo-relative paths only")

  if TRUNC_RE.search(txt_no_code):
    return die("lessons_lint", "lessons-learned.md contains '...'; do not truncate evidence pointers")

  print("lessons_lint: OK")
  return 0

if __name__ == "__main__":
  raise SystemExit(main())

