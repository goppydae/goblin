#!/usr/bin/env python3
"""Lint history artifacts for consistency and path hygiene.

This script validates NDJSON history logs and the agenda_state.json snapshot.
It enforces required keys, ID formats, and evidence path hygiene.
If the history directory is absent, it exits successfully with a warning.
"""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import List, Set, Tuple

import jsonschema
from lint_common import validate_paths

def load_history_schema() -> dict:
  schema_path = Path(__file__).parent.parent / "schemas/history_schema.json"
  return json.loads(schema_path.read_text(encoding="utf-8"))


HISTORY_DIR = Path("artifacts/history")
REQUIRED_ENTRY_KEYS = {"agenda_id", "hypothesis_id", "timestamp", "summary", "evidence"}
HYP_RE = re.compile(r"^HYP-\d{4}$")
AG_RE = re.compile(r"^AG-\d{6}$")
VALID_STATUSES = {"finished", "in-progress", "blocked", "not-started", "unknown"}


def format_error(msg: str) -> str:
  return f"history_lint: ERROR: {msg}"


def validate_evidence_paths(paths: object, ctx: str) -> List[str]:
  errors: List[str] = []
  if not isinstance(paths, list) or not paths:
    return [format_error(f"{ctx} evidence must be a non-empty list of strings")]
  for idx, p in enumerate(paths, start=1):
    if not isinstance(p, str) or not p.strip():
      errors.append(format_error(f"{ctx} evidence entry {idx} must be a non-empty string"))
      continue

    path_error = validate_paths(p)
    if path_error:
      errors.append(format_error(f"{ctx} evidence entry {idx} {path_error}"))

    if Path(p).is_absolute():
      errors.append(format_error(f"{ctx} evidence entry {idx} must be repo-relative (not absolute)"))
    if p.startswith(".."):
      errors.append(format_error(f"{ctx} evidence entry {idx} must not traverse parent directories"))
  return errors


def lint_ndjson_file(path: Path) -> Tuple[List[str], Set[str], Set[str]]:
  errors: List[str] = []
  hyp_ids: Set[str] = set()
  agenda_ids: Set[str] = set()

  lines = path.read_text(encoding="utf-8").splitlines()
  if not lines:
    # Allow empty NDJSON files during initialization
    return errors, hyp_ids, agenda_ids

  schema = load_history_schema()
  for lineno, line in enumerate(lines, start=1):
    if not line.strip():
      errors.append(format_error(f"{path}:{lineno}: blank line not allowed in NDJSON"))
      continue
    try:
      entry = json.loads(line)
    except json.JSONDecodeError as exc:
      errors.append(format_error(f"{path}:{lineno}: invalid JSON: {exc}"))
      continue

    try:
      jsonschema.validate(instance=entry, schema=schema)
    except jsonschema.ValidationError as exc:
      errors.append(format_error(f"{path}:{lineno}: schema validation failed: {exc.message}"))
      continue

    rtype = entry["record_type"]

    # Evidence paths still need custom check (against repo root etc)
    errors.extend(validate_evidence_paths(entry.get("evidence"), f"{path}:{lineno}"))

    # ID tracking for cross-check
    if rtype != "journal":
      agenda_id = entry.get("agenda_id")
      hypothesis_id = entry.get("hypothesis_id")
      if agenda_id: agenda_ids.add(agenda_id)
      if hypothesis_id: hyp_ids.add(hypothesis_id)

  return errors, hyp_ids, agenda_ids


def lint_agenda_state(path: Path, hist_hypotheses: Set[str], hist_agenda: Set[str]) -> List[str]:
  errors: List[str] = []
  try:
    data = json.loads(path.read_text(encoding="utf-8"))
  except Exception as exc:  # noqa: BLE001 - surface JSON parsing issues directly
    return [format_error(f"{path}: invalid JSON: {exc}")]

  if not isinstance(data, dict):
    return [format_error(f"{path}: expected top-level object, got {type(data).__name__}")]

  agenda_items = data.get("agenda_items")
  hypotheses = data.get("hypotheses")
  if not isinstance(agenda_items, list):
    errors.append(format_error(f"{path}: agenda_items must be a list"))
    agenda_items = []
  if not isinstance(hypotheses, list):
    errors.append(format_error(f"{path}: hypotheses must be a list"))
    hypotheses = []

  seen_agenda: Set[str] = set()
  seen_hypotheses: Set[str] = set()

  for idx, item in enumerate(agenda_items, start=1):
    ctx = f"{path}:agenda_items[{idx}]"
    if not isinstance(item, dict):
      errors.append(format_error(f"{ctx}: must be an object"))
      continue

    ag_id = item.get("id")
    if not isinstance(ag_id, str) or not AG_RE.fullmatch(ag_id):
      errors.append(format_error(f"{ctx}: id must match AG-######"))
    elif ag_id in seen_agenda:
      errors.append(format_error(f"{ctx}: duplicate agenda id {ag_id}"))
    else:
      seen_agenda.add(ag_id)

    status = item.get("status")
    if not isinstance(status, str) or status not in VALID_STATUSES:
      errors.append(format_error(f"{ctx}: status must be one of {sorted(VALID_STATUSES)}"))

    hyp_id = item.get("hypothesis_id")
    if hyp_id is not None:
      if not isinstance(hyp_id, str) or not HYP_RE.fullmatch(hyp_id):
        errors.append(format_error(f"{ctx}: hypothesis_id must match HYP-#### when provided"))
      else:
        # Track for cross-check, but don't treat as 'definition' yet
        pass

    if "evidence" in item:
      errors.extend(validate_evidence_paths(item.get("evidence"), f"{ctx}"))

  for idx, item in enumerate(hypotheses, start=1):
    ctx = f"{path}:hypotheses[{idx}]"
    if not isinstance(item, dict):
      errors.append(format_error(f"{ctx}: must be an object"))
      continue

    hyp_id = item.get("id")
    if not isinstance(hyp_id, str) or not HYP_RE.fullmatch(hyp_id):
      errors.append(format_error(f"{ctx}: id must match HYP-####"))
    elif hyp_id in seen_hypotheses:
      errors.append(format_error(f"{ctx}: duplicate hypothesis id {hyp_id}"))
    else:
      seen_hypotheses.add(hyp_id)

    status = item.get("status")
    if not isinstance(status, str) or status not in VALID_STATUSES:
      errors.append(format_error(f"{ctx}: status must be one of {sorted(VALID_STATUSES)}"))

    if "evidence" in item:
      errors.extend(validate_evidence_paths(item.get("evidence"), f"{ctx}"))

  # Cross-check history references
  for ag_id in sorted(hist_agenda - seen_agenda):
    errors.append(format_error(f"{path}: missing agenda_items entry for {ag_id} referenced in history"))
  for hyp_id in sorted(hist_hypotheses - seen_hypotheses):
    errors.append(format_error(f"{path}: missing hypotheses entry for {hyp_id} referenced in history"))

  return errors


def collect_history_files() -> List[Path]:
  return sorted(HISTORY_DIR.glob("*.ndjson"))


def main() -> int:
  if not HISTORY_DIR.exists():
    print("history_lint: history directory not found; skipping")
    return 0

  all_errors: List[str] = []
  hyp_ids: Set[str] = set()
  agenda_ids: Set[str] = set()

  for ndjson_file in collect_history_files():
    errors, hyp_set, ag_set = lint_ndjson_file(ndjson_file)
    all_errors.extend(errors)
    hyp_ids.update(hyp_set)
    agenda_ids.update(ag_set)

  agenda_state = HISTORY_DIR / "agenda_state.json"
  if agenda_state.exists():
    all_errors.extend(lint_agenda_state(agenda_state, hyp_ids, agenda_ids))
  elif hyp_ids or agenda_ids:
    all_errors.append(format_error("artifacts/history/agenda_state.json is required when history entries exist"))

  if all_errors:
    for err in all_errors:
      print(err, file=sys.stderr)
    return 1

  print("history_lint: OK")
  return 0


if __name__ == "__main__":
  raise SystemExit(main())
