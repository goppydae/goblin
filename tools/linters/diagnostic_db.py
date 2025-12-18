#!/usr/bin/env python3
"""Diagnostic Knowledge Base for Linters.

This module provides a mechanism to look up linter rules and remediation strategies
based on the tool name and error message, avoiding the need to search linter source code.
"""

import json
import re
import sys
from pathlib import Path
from typing import Optional, Dict, List, Any

# Locate rules.json relative to this file
RULES_PATH = Path(__file__).parent / "rules.json"

class DiagnosticDB:
    """Interface to the diagnostic rules database."""
    
    def __init__(self, rules_path: Path = RULES_PATH):
        self.rules_path = rules_path
        self._rules: List[Dict[str, Any]] = []
        self._loaded = False

    def load(self) -> None:
        """Load rules from the JSON database."""
        if self._loaded:
            return
            
        if not self.rules_path.exists():
            # If the DB doesn't exist, we just operate with empty rules
            # to avoid crashing the agent if misconfigured.
            return

        try:
            data = json.loads(self.rules_path.read_text(encoding="utf-8"))
            self._rules = data.get("rules", [])
            self._loaded = True
        except Exception as e:
            # Print to stderr but don't crash
            print(f"Error loading diagnostic DB from {self.rules_path}: {e}", file=sys.stderr)

    def lookup(self, tool: str, message: str) -> Optional[Dict[str, Any]]:
        """Find a rule matching the tool and message.
        
        Args:
            tool: The name of the linter tool (e.g., 'agenda_lint').
            message: The error message output by the tool.
            
        Returns:
            The matching rule dictionary or None.
        """
        self.load()
        for rule in self._rules:
            # Filter by tool name first
            if rule.get("tool") != tool:
                continue
            
            # Check message regex
            pattern = rule.get("message_regex")
            if pattern:
                # Use search to find the pattern anywhere in the message
                if re.search(pattern, message):
                    return rule
        return None

def main():
    """CLI interface for testing lookups."""
    if len(sys.argv) < 3:
        print("Usage: diagnostic_db.py <tool> <message>")
        sys.exit(1)
    
    tool = sys.argv[1]
    msg = sys.argv[2]
    
    db = DiagnosticDB()
    rule = db.lookup(tool, msg)
    
    if rule:
        print(json.dumps(rule, indent=2))
        sys.exit(0)
    else:
        print("No match found.", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
