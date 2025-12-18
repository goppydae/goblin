#!/usr/bin/env python3
"""Generate context manifest for prep-context workflow.

Outputs: artifacts/logs/context_manifest.md
"""

import os
import sys
from pathlib import Path
from datetime import datetime, timezone


def read_agentsignore(root: Path) -> list:
    """Read .agentsignore patterns."""
    ignore_file = root / ".agentsignore"
    if not ignore_file.exists():
        return []
    
    patterns = []
    with open(ignore_file) as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith("#"):
                patterns.append(line)
    return patterns


def list_workspace_files(root: Path) -> list:
    """List all files in workspace."""
    files = []
    for dirpath, dirnames, filenames in os.walk(root):
        # Skip .git
        if ".git" in dirnames:
            dirnames.remove(".git")
        
        for filename in filenames:
            filepath = Path(dirpath) / filename
            rel_path = filepath.relative_to(root)
            files.append(str(rel_path))
    
    return sorted(files)


def main():
    root = Path.cwd()
    output_dir = root / "artifacts" / "logs"
    output_dir.mkdir(parents=True, exist_ok=True)
    
    output_file = output_dir / "context_manifest.md"
    
    # Generate manifest
    timestamp = datetime.now(timezone.utc).isoformat()
    patterns = read_agentsignore(root)
    files = list_workspace_files(root)
    
    # Write manifest
    with open(output_file, "w") as f:
        f.write("# Context Manifest\n\n")
        f.write(f"- timestamp: {timestamp}\n")
        f.write("- operating mode: maintenance\n")
        f.write("- .agentsignore: .agentsignore\n")
        f.write(f"- files read: {len(files)}\n\n")
        f.write("## Agent Ignore Patterns\n\n")
        
        if patterns:
            for pattern in patterns:
                f.write(f"- `{pattern}`\n")
        else:
            f.write("*No .agentsignore file found*\n")
        
        f.write(f"\n## Workspace Files\n\n")
        f.write(f"**Total files**: {len(files)}\n\n")
        
        # Sample (first 50 files)
        sample_size = min(50, len(files))
        f.write(f"**Sample** (first {sample_size}):\n\n")
        for filepath in files[:sample_size]:
            f.write(f"- `{filepath}`\n")
        
        if len(files) > sample_size:
            f.write(f"\n*... and {len(files) - sample_size} more files*\n")
    
    print(f"Context manifest generated: {output_file}")


if __name__ == "__main__":
    main()
