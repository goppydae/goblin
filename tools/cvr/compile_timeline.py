#!/usr/bin/env python3
"""Compile Deep Thoughts timeline from journals.

Outputs: artifacts/history/timeline.md
"""

import os
import re
from pathlib import Path
from datetime import datetime

def parse_journal_date(journal_path: Path) -> datetime:
    """Extract date from journal filename or content (stub)."""
    # Assuming filename is runnable-id based or we use file mtime as fallback
    # But ideally runs have IDs like 2025-12-18_fix-login
    # For now, simplistic parsing or mtime
    name = journal_path.stem
    try:
        # Try finding a date at start of filename
        match = re.search(r"(\d{4}-\d{2}-\d{2})", name)
        if match:
            return datetime.fromisoformat(match.group(1))
    except ValueError:
        pass
    return datetime.fromtimestamp(journal_path.stat().st_mtime)

def main():
    root = Path.cwd()
    journal_dir = root / "artifacts" / "journal"
    output_file = root / "artifacts" / "history" / "deep-thoughts.md"
    
    if not journal_dir.exists():
        print(f"No journals found at {journal_dir}")
        return

    journals = []
    for f in journal_dir.glob("*.md"):
        date = parse_journal_date(f)
        journals.append((date, f))
    
    # Sort reverse chronological
    journals.sort(key=lambda x: x[0], reverse=True)
    
    content = ["# Deep Thoughts Timeline\n"]
    content.append("> A narrative reconstruction derived from run artifacts, intended to illustrate a deterministic decision process rather than serve as a primary source of truth.\n")
    
    for date, path in journals:
        try:
            text = path.read_text().strip()
            # Extract title (first line)
            lines = text.splitlines()
            title = lines[0].lstrip("# ").strip() if lines else path.name
            
            content.append(f"## {date.date()} - {title}")
            content.append(f"\n[View Journal]({path.relative_to(root)})\n")
            
            # Extract Summary if present
            summary_found = False
            for line in lines:
                if line.lower().startswith("summary:"):
                    content.append(f"{line}\n")
                    summary_found = True
                    break
            
            if not summary_found:
                 # Fallback: first non-empty line after title
                for line in lines[1:]:
                    if line.strip():
                        content.append(f"{line.strip()}\n")
                        break
            
            content.append("---\n")
            
        except Exception as e:
            print(f"Error reading {path}: {e}")

    output_file.parent.mkdir(parents=True, exist_ok=True)
    output_file.write_text("\n".join(content))
    print(f"Timeline compiled: {output_file}")

if __name__ == "__main__":
    main()
