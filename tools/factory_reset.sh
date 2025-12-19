#!/usr/bin/env bash
set -euo pipefail

# factory_reset.sh - Destructively resets the repository history and artifacts.
# USE WITH CAUTION.

FORCE=false
INCLUDE_INTENT=false
DRY_RUN=false
VERBOSE=false

# Parse arguments
for arg in "$@"; do
  case $arg in
    --force)
      FORCE=true
      shift
      ;;
    --include-intent)
      INCLUDE_INTENT=true
      shift
      ;;
    --dry-run|-n)
      DRY_RUN=true
      shift
      ;;
    --verbose|-v)
      VERBOSE=true
      shift
      ;;
    *)
      # Shift handled args via case, but simple loop doesn't handle values well if strict.
      # For simple flags this works.
      ;;
  esac
done

if [ "$FORCE" != "true" ] && [ "$DRY_RUN" != "true" ]; then
  echo "ERROR: This tool is destructive. You must pass --force to run it."
  echo "Usage: ./tools/factory_reset.sh --force [--include-intent] [--dry-run] [--verbose]"
  echo "  --include-intent: Also delete artifacts/intent/project_intent.md"
  echo "  --dry-run, -n   : Show what would be deleted without doing it"
  echo "  --verbose, -v   : Verbose output"
  exit 1
fi

log() {
  if [ "$VERBOSE" = "true" ] || [ "$DRY_RUN" = "true" ]; then
    echo "$@"
  fi
}

# Helper to remove files/dirs
clean_path() {
  local path="$1"
  local description="$2"

  if [ -e "$path" ]; then
    if [ "$DRY_RUN" = "true" ]; then
      log "[DRY-RUN] Would remove $path ($description)"
    else
      log "Removing $path ($description)..."
      rm -rf "$path"
    fi
  fi
}

# Helper to recreate empty directory and gitkeep
reset_dir() {
  local dir="$1"
  if [ "$DRY_RUN" = "true" ]; then
    log "[DRY-RUN] Would ensure $dir exists and has .gitkeep"
  else
    if [ ! -d "$dir" ]; then
      mkdir -p "$dir"
    fi
    touch "$dir/.gitkeep"
    log "Reset $dir"
  fi
}

echo "initiating FACTORY RESET..."
if [ "$DRY_RUN" = "true" ]; then
  echo "(Mode: DRY-RUN. No changes will be made.)"
fi

# 1. Clear history completely (including runs, deep-thoughts, lessons, etc.)
# We remove the entire directory contents but keep the root structure potentially?
# Actually, deleting the folder and recreating is cleaner.
clean_path "artifacts/history" "History artifacts"
reset_dir "artifacts/history"
reset_dir "artifacts/history/runs"

# 2. Clear journals
clean_path "artifacts/journal" "Journal entries"
reset_dir "artifacts/journal"

# 3. Clear logs
clean_path "artifacts/logs" "Logs"
reset_dir "artifacts/logs"

# 4. Clear diffs
clean_path "artifacts/diffs" "Diffs"
reset_dir "artifacts/diffs"

# 5. Clear test_results
clean_path "artifacts/test_results" "Test results"
reset_dir "artifacts/test_results"

# 6. Reset agent activity ledger
if [ "$DRY_RUN" = "true" ]; then
  log "[DRY-RUN] Would truncate artifacts/agent_activity.jsonl"
else
  if [ -f "artifacts/agent_activity.jsonl" ]; then
     : > "artifacts/agent_activity.jsonl"
     log "Truncated artifacts/agent_activity.jsonl"
  fi
fi

# 7. Intent
if [ "$INCLUDE_INTENT" = "true" ]; then
  clean_path "artifacts/intent/project_intent.md" "Project Intent"
else
  log "Preserving artifacts/intent/project_intent.md"
fi

# 8. Reset AGENDA.md
if [ "$DRY_RUN" = "true" ]; then
  log "[DRY-RUN] Would reset AGENDA.md to default state"
else
  cat > AGENDA.md <<EOF
# Agenda

**Status**: Active

## Active Hypotheses

- None.

## Blockers

- None.

## Deferred Risks

- None.
EOF
  log "Reset AGENDA.md"
fi

echo "Factory reset complete."
