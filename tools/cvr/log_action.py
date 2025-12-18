#!/usr/bin/env python3
"""
log_action.py - Append an event to the Agent Activity Ledger.

Usage:
  python3 tools/cvr/log_action.py --intent <intent> --action <action> [--scope <scope>] [--result <result>] [--evidence <file>...] [--metadata <json_string>]

The ledger is stored at: artifacts/agent_activity.jsonl (NDJSON)
"""

import argparse
import datetime
import json
import os
import sys
from pathlib import Path

LEDGER_PATH = Path("artifacts/agent_activity.jsonl")
MODE_FILE = Path("artifacts/logs/agent_mode.json")

def get_current_mode():
    try:
        if MODE_FILE.exists():
            data = json.loads(MODE_FILE.read_text())
            return data.get("mode", "normal")
    except Exception:
        pass
    return "normal"

def get_actor():
    return os.environ.get("USER", os.environ.get("USERNAME", "unknown"))

def append_entry(intent, action, scope="workspace", result="ok", evidence=None, metadata=None):
    entry = {
        "ts": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
        "actor": get_actor(),
        "intent": intent,
        "scope": scope,
        "action": action,
        "result": result,
    }
    
    # Optional fields
    if evidence:
        entry["evidence"] = evidence
    
    # Add mode to metadata or top level? Historical log didn't have mode, but we previously wanted it.
    # We will add it to entry as it is contextually relevant for the agent.
    # Or keep it if the user wants strict adherence. The user said "I'd like agent_activity.jsonl to have some of the same fields".
    # I'll include 'mode' because it's critical for this specific agent implementation (maintenance vs normal).
    entry["mode"] = get_current_mode()

    if metadata:
        try:
            md_obj = json.loads(metadata)
            if "metadata" in entry:
                 entry["metadata"].update(md_obj)
            else:
                 entry["metadata"] = md_obj
        except json.JSONDecodeError:
            entry["metadata"] = {"raw": metadata}

    # Ensure artifacts directory exists
    LEDGER_PATH.parent.mkdir(parents=True, exist_ok=True)

    # Append to NDJSON
    with open(LEDGER_PATH, "a", encoding="utf-8") as f:
        f.write(json.dumps(entry) + "\n")
    
    print(f"Logged action '{action}' for intent '{intent}' to {LEDGER_PATH}")

def main():
    parser = argparse.ArgumentParser(description="Log an action to the agent activity ledger.")
    parser.add_argument("--intent", required=True, help="High-level goal or task (e.g., 'refactor_auth')")
    parser.add_argument("--action", required=True, help="Verb (e.g., 'modify', 'plan', 'verify')")
    parser.add_argument("--scope", default="workspace", help="Affected subsystem or file")
    parser.add_argument("--result", default="ok", help="Outcome (ok, fail)")
    parser.add_argument("--evidence", nargs="*", help="List of artifact paths")
    parser.add_argument("--metadata", help="Optional JSON string with extra details")
    
    # Backwards compatibility check (if old args used, though we are updating workflows)
    # No strict back-compat needed if we update all workflows simultaneously.
    
    args = parser.parse_args()
    
    try:
        append_entry(
            intent=args.intent,
            action=args.action,
            scope=args.scope,
            result=args.result,
            evidence=args.evidence,
            metadata=args.metadata
        )
    except Exception as e:
        print(f"ERROR: Failed to log action: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
