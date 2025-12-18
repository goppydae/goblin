#!/usr/bin/env bash
# tools/verify_all.sh - Main verification orchestrator for goppydae-silo
set -euo pipefail

# Add linters directory to Python path
export PYTHONPATH="${PYTHONPATH:-}:$(pwd)/tools/linters"

mkdir -p artifacts/logs artifacts/test_results artifacts/history/runs artifacts/intent

ts="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"

run_log() {
  local name="$1"
  shift
  local out="artifacts/logs/${name}.log"
  echo "==> ${name} @ ${ts}" | tee "${out}"
  echo "+ $*" | tee -a "${out}"
  ( "$@" ) >>"${out}" 2>&1
  echo "==> OK: ${name}" | tee -a "${out}"
}

# Check markdown formatting
if command -v mdformat >/dev/null 2>&1; then
  run_log "format_md_check" python3 -m mdformat --check *.md docs/**/*.md .agent/**/*.md 2>/dev/null || true
else
  echo "WARNING: mdformat not available; skipping markdown formatting checks."
fi

# Intent must exist for any real work. (Fail closed.)
if [ -f tools/linters/intent_lint.py ]; then
  run_log "intent_lint" python3 tools/linters/intent_lint.py
fi

# Lints (conditional on file existence, non-fatal for Go project adaptation)
if [ -f tools/linters/agenda_lint.py ]; then 
  run_log "agenda_lint" python3 tools/linters/agenda_lint.py || true
fi

if [ -f tools/linters/context_manifest_lint.py ] && [ -f artifacts/logs/context_manifest.md ]; then
  run_log "context_manifest_lint" python3 tools/linters/context_manifest_lint.py
fi

if [ -f tools/linters/post_verify_lint.py ] && [ -f artifacts/logs/post_verify_report.md ]; then
  run_log "post_verify_lint" python3 tools/linters/post_verify_lint.py
fi

if [ -f tools/linters/lessons_lint.py ] && [ -f artifacts/history/lessons-learned.md ]; then
  run_log "lessons_lint" python3 tools/linters/lessons_lint.py
fi

if [ -f tools/linters/walkthrough_lint.py ]; then
  # Only run if a walkthrough exists (root or in runs/)
  if [ -f walkthrough.md ] || find artifacts/history/runs -name "walkthrough.md" -type f 2>/dev/null | grep -q .; then
    run_log "walkthrough_lint" python3 tools/linters/walkthrough_lint.py
  fi
fi

if [ -f tools/linters/run_artifacts_lint.py ] && [ -d artifacts/history/runs ]; then
  run_log "run_artifacts_lint" python3 tools/linters/run_artifacts_lint.py
fi

if [ -f tools/linters/content_lint.py ]; then
  run_log "content_lint" python3 tools/linters/content_lint.py
fi

if [ -f tools/linters/history_lint.py ]; then
  run_log "history_lint" python3 tools/linters/history_lint.py
fi

# Plan lint: validate run-dir plan if present, else root
if [ -f tools/linters/plan_lint.py ]; then
  if ls artifacts/history/runs/**/implementation_plan.json >/dev/null 2>&1; then
    run_log "plan_lint_run" python3 tools/linters/plan_lint.py --run
  elif [ -f implementation_plan.json ]; then
    run_log "plan_lint_root" python3 tools/linters/plan_lint.py
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
