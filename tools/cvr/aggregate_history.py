#!/usr/bin/env python3
import argparse
import json
import os
import re
import sys
from pathlib import Path
from typing import Dict, Iterable, List, Optional, Tuple

# Import canonical paths - scripts run from repo root
sys.path.insert(0, str(Path(__file__).resolve().parent.parent.parent))
from tools.cvr import paths

DEFAULT_HISTORY_PATH = paths.HISTORY_NDJSON
DEEP_THOUGHTS_PATH = paths.DEEP_THOUGHTS

HEADER = "# Deep Thoughts: A Journal Timeline\n\n*(Reverse chronological order)*\n"


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Build history and narrative from run artifacts.")
    parser.add_argument(
        "--output",
        type=Path,
        default=DEFAULT_HISTORY_PATH,
        help="Path to write the history NDJSON (default: artifacts/history/history.ndjson)",
    )
    parser.add_argument(
        "--narrative",
        type=Path,
        default=DEEP_THOUGHTS_PATH,
        help="Path to write the narrative markdown (default: artifacts/history/deep-thoughts.md)",
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="Do not write files; exit with non-zero status if output would change",
    )
    return parser.parse_args(argv)


def load_history(path: Path) -> List[Dict]:
    if not path.exists():
        return []
    records: List[Dict] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError:
            continue  # best effort
    return records


def normalize_status(status: Optional[str]) -> str:
    if not status:
        return "active"
    return str(status).strip().lower() or "active"


def split_anchor(value: str) -> Tuple[str, str]:
    if "#" in value:
        base, anchor = value.split("#", 1)
        return base, f"#{anchor}"
    return value, ""


def normalize_evidence_entry(entry: str, repo_root: Path) -> Optional[str]:
    entry = entry.strip()
    if not entry or re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*://", entry):
        return None

    base, anchor = split_anchor(entry)
    path_part = Path(base)

    if path_part.is_absolute():
        try:
            rel = path_part.relative_to(repo_root)
        except ValueError:
            return None
        normalized = rel.as_posix()
    else:
        normalized = path_part.as_posix()

    if not normalized:
        return None
    return f"{normalized}{anchor}"


def extract_repo_paths_from_text(text: str, repo_root: Path) -> List[str]:
    candidates = set()
    link_pattern = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
    for match in link_pattern.finditer(text):
        normalized = normalize_evidence_entry(match.group(1), repo_root)
        if normalized:
            candidates.add(normalized)

    inline_pattern = re.compile(r"\b((?:docs|artifacts|tests|tools|src)/[^\s)]+)")
    for match in inline_pattern.finditer(text):
        normalized = normalize_evidence_entry(match.group(1), repo_root)
        if normalized:
            candidates.add(normalized)
    return sorted(candidates)


def pick_first_seen(existing: Optional[str], new: Optional[str]) -> Optional[str]:
    if not existing or existing == "unknown":
        return new
    if not new or new == "unknown":
        return existing
    return min(existing, new)


def pick_last_seen(existing: Optional[str], new: Optional[str]) -> Optional[str]:
    if not existing or existing == "unknown":
        return new
    if not new or new == "unknown":
        return existing
    return max(existing, new)


def merge_evidence(existing: Iterable[str], incoming: Iterable[str]) -> List[str]:
    merged = {e for e in existing if e}
    merged.update(e for e in incoming if e)
    return sorted(merged)


def merge_hypothesis_records(existing: Optional[Dict], new: Dict) -> Dict:
    if existing is None:
        return new

    merged = dict(existing)
    merged["record_type"] = "hypothesis"
    merged["id"] = new.get("id", merged.get("id"))
    existing_claim = merged.get("claim", "")
    new_claim = (new.get("claim") or "").strip()
    if new_claim and (not existing_claim or len(new_claim) > len(existing_claim)):
        merged["claim"] = new_claim

    existing_status = normalize_status(merged.get("status"))
    incoming_status = normalize_status(new.get("status"))
    if incoming_status == "active" and existing_status != "active":
        merged["status"] = existing_status
    else:
        merged["status"] = incoming_status
    merged["first_seen_run"] = pick_first_seen(merged.get("first_seen_run"), new.get("first_seen_run"))
    merged["last_seen_run"] = pick_last_seen(merged.get("last_seen_run"), new.get("last_seen_run"))
    merged["evidence"] = merge_evidence(merged.get("evidence", []), new.get("evidence", []))
    
    # Required field mapping for history_lint
    merged["timestamp"] = new.get("timestamp", merged.get("timestamp", ""))
    merged["summary"] = merged.get("claim", "")
    merged["hypothesis_id"] = merged["id"]
    merged["agenda_id"] = "AG-000000" # Placeholder if mixing types, or handle in merge_records

    # Clean up fields
    if "first_seen_run" in merged and not merged["timestamp"]:
        merged["timestamp"] = merged["first_seen_run"] # Fallback

    return merged


def make_hypothesis_record(
    hyp_id: str,
    claim: Optional[str],
    status: Optional[str],
    run_name: Optional[str],
    evidence: Iterable[str],
) -> Dict:
    return {
        "record_type": "hypothesis",
        "id": hyp_id, # Internal ID
        "hypothesis_id": hyp_id, # Lint required ID
        "agenda_id": "AG-000000", # Lint required ID
        "summary": claim or "", # Lint required summary
        "claim": claim or "", 
        "status": normalize_status(status),
        "timestamp": run_name or "",
        "first_seen_run": run_name,
        "last_seen_run": run_name,
        "evidence": sorted({e for e in evidence if e}),
    }


def parse_plan(run_dir: Path, repo_root: Path) -> List[Dict]:
    plan_path = run_dir / "implementation_plan.json"
    if not plan_path.exists():
        return []

    data = json.loads(plan_path.read_text(encoding="utf-8"))
    items = data.get("items", [])
    run_name = run_dir.name
    records: List[Dict] = []
    for item in items:
        hyp_id = item.get("id")
        if not hyp_id:
            continue
        claim = item.get("hypothesis", "")
        status = item.get("status")
        evidence_items = item.get("evidence", {}).get("required_artifacts", [])
        normalized_evidence = sorted(
            {
                e
                for e in (
                    normalize_evidence_entry(ev, repo_root)
                    for ev in evidence_items
                )
                if e
            }
        )
        records.append(make_hypothesis_record(hyp_id, claim, status, run_name, normalized_evidence))
    return records


def parse_id_claim_pairs_from_lines(lines: List[str]) -> Dict[str, str]:
    pairs: Dict[str, str] = {}
    pattern = re.compile(r"\b(HYP-[0-9]{4,})\b[^\n]*?[\:\-\u2013]\s*(.+)")
    for line in lines:
        match = pattern.search(line)
        if match:
            pairs[match.group(1)] = match.group(2).strip()
    return pairs


def parse_walkthrough(run_dir: Path, repo_root: Path) -> List[Dict]:
    walkthrough_path = run_dir / "walkthrough.md"
    if not walkthrough_path.exists():
        return []
    text = walkthrough_path.read_text(encoding="utf-8")
    ids = sorted(set(re.findall(r"HYP-[0-9]{4,}", text)))
    if not ids:
        return []
    lines = text.splitlines()
    id_claim_map = parse_id_claim_pairs_from_lines(lines)
    fallback_claim = None
    for line in lines:
        if "Hypothesis:" in line:
            fallback_claim = line.split("Hypothesis:", 1)[1].strip()
            break

    evidence = extract_repo_paths_from_text(text, repo_root)
    run_name = run_dir.name
    records = []
    for hyp_id in ids:
        claim = id_claim_map.get(hyp_id, fallback_claim)
        records.append(make_hypothesis_record(hyp_id, claim, None, run_name, evidence))
    return records


def parse_post_verify_report(run_dir: Path, repo_root: Path) -> List[Dict]:
    report_path = run_dir / "post_verify_report.md"
    if not report_path.exists():
        return []
    text = report_path.read_text(encoding="utf-8")
    run_name = run_dir.name

    # Match both old and new run ID formats:
    # Old: YYYY-MM-DD_HHMMSS or YYYY-MM-DD_HHMMSS-HYP-####
    # New: YYYY-MM-DD-HH-MM-SS or YYYY-MM-DD-HH-MM-SS-HYP-####
    run_match = re.search(
        r"Run ID:\s*([0-9]{4}-[0-9]{2}-[0-9]{2}[-_][0-9]{2}[-:]?[0-9]{2}[-:]?[0-9]{2}(?:-[A-Z]+-[0-9]{4,})?)",
        text
    )
    hyp_id = None
    if run_match:
        run_id = run_match.group(1)
        # Extract hypothesis ID if present (works for both formats)
        if "-HYP-" in run_id or "_HYP-" in run_id:
            # Split on either - or _ before HYP
            parts = re.split(r"[-_](?=HYP-)", run_id, maxsplit=1)
            if len(parts) == 2:
                hyp_id = parts[1]
    if hyp_id is None:
        m = re.search(r"(HYP-[0-9]{4,})", text)
        if m:
            hyp_id = m.group(1)
    if hyp_id is None:
        return []

    claim_match = re.search(r"Hypothesis:\s*(.+)", text)
    claim = claim_match.group(1).strip() if claim_match else None
    status_match = re.search(r"Status:\s*([A-Za-z\-]+)", text)
    status = status_match.group(1).strip() if status_match else None
    evidence = extract_repo_paths_from_text(text, repo_root)
    return [make_hypothesis_record(hyp_id, claim, status, run_name, evidence)]


def collect_hypotheses(runs_dir: Path, repo_root: Path) -> List[Dict]:
    records: Dict[str, Dict] = {}
    if not runs_dir.exists():
        return []
    for run_dir in sorted(p for p in runs_dir.iterdir() if p.is_dir()):
        updates: List[Dict] = []
        updates.extend(parse_plan(run_dir, repo_root))
        updates.extend(parse_walkthrough(run_dir, repo_root))
        updates.extend(parse_post_verify_report(run_dir, repo_root))
        for rec in updates:
            hyp_id = rec.get("id")
            if not hyp_id:
                continue
            records[hyp_id] = merge_hypothesis_records(records.get(hyp_id), rec)
    return list(records.values())


def collect_agenda_records(agenda_path: Path, repo_root: Path) -> List[Dict]:
    if not agenda_path.exists():
        return []
    data = json.loads(agenda_path.read_text(encoding="utf-8"))
    items = data.get("items") or data.get("agenda") or data.get("records") or []

    records: List[Dict] = []
    for item in items:
        item_id = item.get("id") or item.get("hypothesis_id")
        if not item_id:
            continue
        evidence_entries = item.get("evidence", [])
        if isinstance(evidence_entries, str):
            evidence_entries = [evidence_entries]
        normalized_evidence = [
            e
            for e in (
                normalize_evidence_entry(ev, repo_root)
                for ev in evidence_entries
            )
            if e
        ]
        records.append(
            {
                "record_type": "agenda",
                "id": item_id,
                "agenda_id": item_id,
                "hypothesis_id": item.get("hypothesis_id", "HYP-0000"),
                "summary": item.get("summary", ""),
                "status": normalize_status(item.get("status")),
                "timestamp": item.get("last_seen_run", "unknown"),
                "first_seen_run": item.get("first_seen_run", "unknown"),
                "last_seen_run": item.get("last_seen_run", "unknown"),
                "evidence": sorted(set(normalized_evidence)),
            }
        )
    return records


def collect_journal_entries(repo_root: Path) -> Tuple[List[Dict], str]:
    journal_dir = repo_root / "artifacts/journal"
    if not journal_dir.exists():
        return [], ""

    records: List[Dict] = []
    narrative_buffer: List[str] = [HEADER]
    
    # Sort reverse-chrono for narrative
    files = sorted(journal_dir.glob("*.md"), reverse=True)
    
    for md_file in files:
        run_id = md_file.stem
        content = md_file.read_text(encoding="utf-8")
        
        # 1. Add to narrative
        narrative_buffer.append(f"\n## {run_id}\n\n{content}\n")
        
        # 2. Add to history records
        # Use filename as evidence
        rel_path = md_file.relative_to(repo_root).as_posix()
        summary = f"Journal entry for {run_id}"
        
        records.append({
            "record_type": "journal",
            "timestamp": run_id,
            "summary": summary,
            "evidence": [rel_path],
            # Pass validation
            "agenda_id": "AG-000000",
            "hypothesis_id": "HYP-0000"
        })
        
    return records, "\n".join(narrative_buffer).strip() + "\n"


def merge_agenda_records(existing: Optional[Dict], new: Dict) -> Dict:
    if existing is None:
        return new
    merged = dict(existing)
    merged["record_type"] = "agenda"
    merged["id"] = new.get("id", merged.get("id"))
    merged["summary"] = new.get("summary") or merged.get("summary") or ""
    merged["status"] = normalize_status(new.get("status") or merged.get("status"))
    merged["first_seen_run"] = pick_first_seen(merged.get("first_seen_run"), new.get("first_seen_run")) or "unknown"
    merged["last_seen_run"] = pick_last_seen(merged.get("last_seen_run"), new.get("last_seen_run")) or "unknown"
    merged["evidence"] = merge_evidence(merged.get("evidence", []), new.get("evidence", []))
    
    # Required field mapping
    merged["timestamp"] = new.get("timestamp", merged.get("timestamp", "unknown"))
    merged["agenda_id"] = merged["id"]
    merged["hypothesis_id"] = merged.get("hypothesis_id", "HYP-0000")

    return merged


def merge_records(existing: List[Dict], new: List[Dict]) -> List[Dict]:
    merged: Dict[Tuple[str, str], Dict] = {}

    def merge_one(record: Dict):
        key = (record.get("record_type"), record.get("id"))
        if record.get("record_type") == "hypothesis":
            merged[key] = merge_hypothesis_records(merged.get(key), record)
        elif record.get("record_type") == "agenda":
            merged[key] = merge_agenda_records(merged.get(key), record)
        else:
            merged[key] = record

    for rec in existing:
        merge_one(rec)
    for rec in new:
        merge_one(rec)

    return sorted(
        merged.values(),
        key=lambda r: (r.get("timestamp", ""), r.get("id") or "", r.get("record_type") or ""),
    )


def main(argv: Optional[List[str]] = None) -> int:
    args = parse_args(argv)
    repo_root = Path.cwd()
    history_path = args.output
    narrative_path = args.narrative

    existing = load_history(history_path)
    runs_dir = repo_root / paths.RUNS_DIR
    
    hyp_records = collect_hypotheses(runs_dir, repo_root)
    agenda_records = collect_agenda_records(repo_root / paths.AGENDA_STATE, repo_root)
    journal_records, narrative_text = collect_journal_entries(repo_root)

    # Filter out existing journals from history so we can regenerate them fully
    # (Journal entries are immutable per run ID, so we can just replace them)
    existing_without_journals = [r for r in existing if r.get("record_type") != "journal"]
    
    # Merge existing (hyp/agenda) with new (hyp/agenda)
    merged_core = merge_records(existing_without_journals, hyp_records + agenda_records)
    
    # Append journals (sorted by timestamp)
    final_records = merged_core + journal_records
    # Sort final records for output
    final_records.sort(key=lambda r: r.get("timestamp", ""))

    # Generate history.md content
    history_lines = [
        "# Execution History",
        "",
        "This file tracks all execution runs in chronological order.",
        "",
        "| Run ID | Timestamp | Status | Summary | Evidence |",
        "| :--- | :--- | :--- | :--- | :--- |",
    ]
    
    # Group by timestamp (run_name)
    runs_seen = {}
    for rec in final_records:
        ts = rec.get("timestamp")
        if not ts or ts == "unknown": continue
        if ts not in runs_seen:
            runs_seen[ts] = {"status": "SUCCESS", "summary": "", "evidence": set()}
        
        if rec.get("record_type") == "hypothesis":
            runs_seen[ts]["status"] = rec.get("status", "SUCCESS")
            runs_seen[ts]["summary"] = rec.get("summary", "")
            runs_seen[ts]["evidence"].update(rec.get("evidence", []))
        elif rec.get("record_type") == "journal":
            runs_seen[ts]["evidence"].update(rec.get("evidence", []))

    history_md_path = repo_root / paths.HISTORY_MD
    history_dir = history_md_path.parent

    for run_ts in sorted(runs_seen.keys()):
        run_info = runs_seen[run_ts]
        
        evidence_links = []
        for ev in sorted(run_info["evidence"]):
            # Calculate relative path from artifacts/history/ to the evidence
            try:
                ev_path = repo_root / ev
                rel_ev = os.path.relpath(ev_path, history_dir)
            except Exception:
                rel_ev = ev
            
            name = Path(ev).name
            evidence_links.append(f"[{name}]({rel_ev})")
        
        evidence_str = ", ".join(evidence_links)
        safe_summary = run_info['summary'].replace("|", "\\|")
        
        history_lines.append(
            f"| {run_ts} | {run_ts} | {run_info['status'].upper()} | {safe_summary} | {evidence_str} |"
        )

    history_md_text = "\n".join(history_lines).strip() + "\n"
    output_lines = [json.dumps(rec, sort_keys=True, separators=(",", ":")) for rec in final_records]
    output_text = "\n".join(output_lines)
    if output_lines:
        output_text += "\n"

    history_md_path = repo_root / paths.HISTORY_MD
    
    if args.check:
        fail = False
        if history_path.exists():
            if history_path.read_text(encoding="utf-8") != output_text:
                fail = True
        elif output_lines:
            fail = True
            
        if narrative_path.exists():
            if narrative_path.read_text(encoding="utf-8") != narrative_text:
                fail = True
        elif narrative_text:
            fail = True

        if history_md_path.exists():
            if history_md_path.read_text(encoding="utf-8") != history_md_text:
                fail = True
        elif history_md_text:
            fail = True
            
        return 1 if fail else 0

    history_path.parent.mkdir(parents=True, exist_ok=True)
    history_path.write_text(output_text, encoding="utf-8")
    
    narrative_path.parent.mkdir(parents=True, exist_ok=True)
    narrative_path.write_text(narrative_text, encoding="utf-8")

    history_md_path.write_text(history_md_text, encoding="utf-8")
    
    print(f"Wrote {len(output_lines)} records to {history_path}")
    print(f"Wrote narrative to {narrative_path}")
    print(f"Wrote history index to {history_md_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
