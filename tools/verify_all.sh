#!/usr/bin/env bash
# tools/verify_all.sh - Main verification orchestrator for goppydae-silo
set -euo pipefail

# Add linters and CVR directories to Python path
if [ -d tools/cvr ]; then
  export PYTHONPATH="${PYTHONPATH:-}:$(pwd)/tools/cvr:$(pwd)/tools/cvr/linters"
elif [ -d tools/linters ]; then
  export PYTHONPATH="${PYTHONPATH:-}:$(pwd)/tools/linters"
fi

mkdir -p artifacts/logs artifacts/test_results artifacts/history/runs artifacts/intent

ts="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

# CVR directory (Canonical Verification Runtime)
# If forbidden in normal mode, this script will degrade gracefully.
CVR_DIR="tools/cvr"
[ -d "$CVR_DIR" ] || CVR_DIR="tools"

run_log() {
  local name="$1"
  shift
  local out="artifacts/logs/${name}.log"
  echo "==> ${name} @ ${ts}" | tee "${out}"
  echo "+ $*" | tee -a "${out}"
  # Execute and capture exit code
  if ( "$@" ) >>"${out}" 2>&1; then
    echo "==> OK: ${name}" | tee -a "${out}"
  else
    local code=$?
    echo "==> FAILED: ${name} (exit code ${code})" | tee -a "${out}"
    return "${code}"
  fi
}

# Check markdown formatting
if [ -f "$CVR_DIR/format_md.py" ]; then
  run_log "format_md_check" python3 "$CVR_DIR/format_md.py" --check
fi

# Intent must exist for any real work. (Fail closed.)
if [ -f "$CVR_DIR/linters/intent_lint.py" ]; then
  run_log "intent_lint" python3 "$CVR_DIR/linters/intent_lint.py"
fi

# Lints (conditional on file existence)
if [ -f "$CVR_DIR/linters/agenda_lint.py" ]; then 
  run_log "agenda_lint" python3 "$CVR_DIR/linters/agenda_lint.py"
fi

if [ -f "$CVR_DIR/linters/context_manifest_lint.py" ] && [ -f artifacts/logs/context_manifest.md ]; then
  run_log "context_manifest_lint" python3 "$CVR_DIR/linters/context_manifest_lint.py"
fi

if [ -f "$CVR_DIR/linters/post_verify_lint.py" ] && [ -f artifacts/logs/post_verify_report.md ]; then
  run_log "post_verify_lint" python3 "$CVR_DIR/linters/post_verify_lint.py"
fi

if [ -f "$CVR_DIR/linters/lessons_lint.py" ] && [ -f artifacts/history/lessons-learned.md ]; then
  run_log "lessons_lint" python3 "$CVR_DIR/linters/lessons_lint.py"
fi

if [ -f "$CVR_DIR/linters/walkthrough_lint.py" ]; then
  # Only run if a walkthrough exists (root or in runs/)
  if [ -f walkthrough.md ] || find artifacts/history/runs -name "walkthrough.md" -type f 2>/dev/null | grep -q .; then
    run_log "walkthrough_lint" python3 "$CVR_DIR/linters/walkthrough_lint.py"
  fi
fi

if [ -f "$CVR_DIR/linters/run_artifacts_lint.py" ] && [ -d artifacts/history/runs ]; then
  run_log "run_artifacts_lint" python3 "$CVR_DIR/linters/run_artifacts_lint.py"
fi

if [ -f "$CVR_DIR/linters/workflow_intent_lint.py" ]; then
  run_log "workflow_intent_lint" python3 "$CVR_DIR/linters/workflow_intent_lint.py"
fi

if [ -f "$CVR_DIR/linters/template_baseline_lint.py" ]; then
  run_log "template_baseline_lint" python3 "$CVR_DIR/linters/template_baseline_lint.py"
fi

if [ -f "$CVR_DIR/linters/journal_lint.py" ] && [ -d artifacts/journal ]; then
  run_log "journal_lint" python3 "$CVR_DIR/linters/journal_lint.py"
fi

if [ -f "$CVR_DIR/linters/evidence_location_lint.py" ]; then
  run_log "evidence_location_lint" python3 "$CVR_DIR/linters/evidence_location_lint.py"
fi

if [ -f "$CVR_DIR/linters/panic_style_lint.py" ]; then
  run_log "panic_style_lint" python3 "$CVR_DIR/linters/panic_style_lint.py"
fi

if [ -f "$CVR_DIR/linters/content_lint.py" ]; then
  run_log "content_lint" python3 "$CVR_DIR/linters/content_lint.py"
fi

if [ -f "$CVR_DIR/linters/history_lint.py" ]; then
  run_log "history_lint" python3 "$CVR_DIR/linters/history_lint.py"
fi

# Plan lint: validate run-dir plan if present, else root
if [ -f "$CVR_DIR/linters/plan_lint.py" ]; then
  if ls artifacts/history/runs/**/implementation_plan.json >/dev/null 2>&1; then
    run_log "plan_lint_run" python3 "$CVR_DIR/linters/plan_lint.py" --run
  elif [ -f implementation_plan.json ]; then
    run_log "plan_lint_root" python3 "$CVR_DIR/linters/plan_lint.py"
  fi
fi

# Go-specific verification
if [ -x tools/verify_go.sh ]; then
  run_log "go_verification" tools/verify_go.sh
fi

# Project tests
if [ -x tools/test.sh ]; then
  run_log "project_tests" tools/test.sh
else
  echo "verify_all: tools/test.sh not present/executable; skipping project tests"
fi

echo "verify_all: OK"
