#!/usr/bin/env python3
import re
import sys
from pathlib import Path
from typing import List, Tuple
from lint_common import die

# Minimum word counts for key artifacts
MIN_WORDS = {
    "walkthrough.md": 100,
    "implementation_plan.md": 50,
}

# Required non-empty sections for specific files
REQUIRED_SECTIONS = {
    "walkthrough.md": ["## Changes", "## Verification Results"],
    "implementation_plan.md": ["## Proposed Changes", "## Verification Plan"],
}

def count_words(text: str) -> int:
    """Count words in text, ignoring code blocks."""
    # Remove code blocks
    text = re.sub(r"```.*?```", "", text, flags=re.DOTALL)
    # Remove inline code
    text = re.sub(r"`[^`]+`", "", text)
    # Simple split by whitespace
    return len(text.split())

def check_structure(path: Path, text: str) -> List[str]:
    """Check if file has required sections."""
    errors = []
    filename = path.name
    if filename in REQUIRED_SECTIONS:
        for section in REQUIRED_SECTIONS[filename]:
            if section not in text:
                errors.append(f"missing required section: '{section}'")
    return errors

def check_word_count(path: Path, text: str) -> List[str]:
    """Check if file satisfies minimum word count."""
    errors = []
    filename = path.name
    if filename in MIN_WORDS:
        count = count_words(text)
        min_count = MIN_WORDS[filename]
        if count < min_count:
            errors.append(f"word count {count} < minimum {min_count}")
    return errors

def main() -> int:
    # Scan for relevant markdown files in current dir and artifacts/history/runs
    files_to_check = []
    
    # Check root artifacts if they exist
    for name in MIN_WORDS.keys():
        p = Path(name)
        if p.exists():
            files_to_check.append(p)
            
    # Check artifacts in runs
    if Path("artifacts/history/runs").exists():
        for run_dir in Path("artifacts/history/runs").iterdir():
            if run_dir.is_dir():
                for name in MIN_WORDS.keys():
                    p = run_dir / name
                    if p.exists():
                        files_to_check.append(p)

    if not files_to_check:
        print("content_lint: no relevant artifacts found to check")
        return 0

    failed = False
    for p in files_to_check:
        try:
            text = p.read_text(encoding="utf-8")
        except Exception as e:
            print(f"content_lint: failed to read {p}: {e}")
            failed = True
            continue

        errors = check_structure(p, text) + check_word_count(p, text)
        
        if errors:
            print(f"FAIL: {p}: {', '.join(errors)}")
            failed = True

    if failed:
        return die("content_lint", "content checks failed")

    print("content_lint: OK")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
