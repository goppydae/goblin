#!/usr/bin/env python3
"""Format and lint Markdown files.

Usage:
  python3 tools/format_md.py          # Format (rewrite) all .md files
  python3 tools/format_md.py --check  # Check formatting (exit 1 if changed)

Dependencies:
  - mdformat (Python)
  - markdownlint-cli2 (Node.js) via subprocess
"""

import sys
import subprocess
import os
from pathlib import Path
from typing import List

# mdformat imports
try:
    import mdformat
    from mdformat._cli import run as mdformat_cli
except ImportError:
    print("ERROR: mdformat not found. Run: pip install -r requirements-verify.txt", file=sys.stderr)
    sys.exit(1)


def find_markdown_files(root: Path) -> List[Path]:
    """Find all .md files in the repo, respecting .agentsignore if possible
    (but for now just simple globbing excluding common patterns)."""
    # Simple rigorous glob implementation
    md_files = []
    for root_dir, dirs, files in os.walk(root):
        # Skip git, hidden, vendor, and build context dirs
        if ".git" in dirs:
            dirs.remove(".git")
        if ".venv" in dirs:
            dirs.remove(".venv")
        if "node_modules" in dirs:
            dirs.remove("node_modules")
        if "vendor" in dirs:
            dirs.remove("vendor")
        if "scenarios" in dirs:
            dirs.remove("scenarios")
            
        for f in files:
            if f.endswith(".md"):
                md_files.append(Path(root_dir) / f)
    return md_files


def run_markdownlint(check_only: bool) -> int:
    """Run markdownlint-cli2."""
    cmd = ["markdownlint-cli2", "**/*.md", "#node_modules", "#vendor", "#scenarios"]
    
    # Check if tool exists
    if not shutil.which("markdownlint-cli2"):
        # Fail if missing? Or warn?
        # Per plan: "fail with a message directing the user to run tools/check_tools.sh"
        print("ERROR: markdownlint-cli2 not found.", file=sys.stderr)
        print("Please run 'tools/check_tools.sh' to verify your environment.", file=sys.stderr)
        return 1

    # markdownlint-cli2 doesn't have a specific "rewrite" mode via CLI args in the same way,
    # but it auto-fixes if config enabled (default does not always autofix).
    # We will just run it. If it returns non-zero, it means lint errors.
    
    print("==> Running markdownlint-cli2...")
    try:
        subprocess.run(cmd, check=True)
        return 0
    except subprocess.CalledProcessError:
        return 1


import shutil

def main() -> int:
    check_mode = "--check" in sys.argv
    
    repo_root = Path.cwd()
    
    print(f"==> Running mdformat ({'check' if check_mode else 'write'})...")
    
    # We'll use mdformat's CLI entry point for simplicity as it handles extensions well
    files = [str(f.relative_to(repo_root)) for f in find_markdown_files(repo_root)]
    
    if not files:
        print("No markdown files found.")
        return 0

    base_cmd = [sys.executable, "-m", "mdformat"]
    if check_mode:
        base_cmd.append("--check")
    
    # Add extensions explicitly if not auto-detected from config?
    # mdformat usually picks up installed plugins.
    
    cmd = base_cmd + files
    
    try:
        subprocess.run(cmd, check=True)
    except subprocess.CalledProcessError:
        print("FAIL: mdformat found issues.")
        return 1

    # Step 2: Lint
    # Linting is read-only check usually.
    return run_markdownlint(check_mode)


if __name__ == "__main__":
    sys.exit(main())
