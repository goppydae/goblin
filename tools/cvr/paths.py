#!/usr/bin/env python3
"""Canonical artifact paths for the ADK Verification Runtime.

This module defines the single source of truth for all artifact paths.
All Python scripts SHOULD import from this module rather than hardcoding paths.
"""

from pathlib import Path

# Root directories
ARTIFACTS_ROOT = Path("artifacts")

# Top-level artifacts
AGENT_ACTIVITY_LOG = ARTIFACTS_ROOT / "agent_activity.jsonl"

# Intent
INTENT_DIR = ARTIFACTS_ROOT / "intent"
PROJECT_INTENT = INTENT_DIR / "project_intent.md"

# History
HISTORY_DIR = ARTIFACTS_ROOT / "history"
HISTORY_NDJSON = HISTORY_DIR / "history.ndjson"
HISTORY_MD = HISTORY_DIR / "history.md"
DEEP_THOUGHTS = HISTORY_DIR / "deep-thoughts.md"
LESSONS_LEARNED = HISTORY_DIR / "lessons-learned.md"
RUNS_DIR = HISTORY_DIR / "runs"
AGENDA_STATE = HISTORY_DIR / "agenda_state.json"

# Journal
JOURNAL_DIR = ARTIFACTS_ROOT / "journal"

# Logs
LOGS_DIR = ARTIFACTS_ROOT / "logs"
AGENT_MODE_FILE = LOGS_DIR / "agent_mode.json"
CONTEXT_MANIFEST = LOGS_DIR / "context_manifest.md"
POST_VERIFY_REPORT = LOGS_DIR / "post_verify_report.md"

# Evidence directories
DIFFS_DIR = ARTIFACTS_ROOT / "diffs"
TEST_RESULTS_DIR = ARTIFACTS_ROOT / "test_results"
