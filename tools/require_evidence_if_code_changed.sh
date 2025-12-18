#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${GITHUB_BASE_REF:-}" ]]; then
  git fetch origin "${GITHUB_BASE_REF}" --depth=1
  BASE="origin/${GITHUB_BASE_REF}"
  HEAD="HEAD"
else
  BASE="HEAD~1"
  HEAD="HEAD"
fi

CHANGED="$(git diff --name-only "${BASE}" "${HEAD}" || true)"

# Adapted for Go project: exclude scenarios, infrastructure, and docs
# Include: Go code changes in core project (Magefile.go, etc.)
NON_CODE_RE='^(artifacts/|\.agent/|tools/|\.github/|scenarios/.*\.(sh|yaml|yml)$|.*\.md$|\.gitignore$|flake\.(nix|lock)$)'

CODE_CHANGED=0
while IFS= read -r f; do
  [[ -z "${f}" ]] && continue
  if [[ ! "${f}" =~ ${NON_CODE_RE} ]]; then
    CODE_CHANGED=1
    break
  fi
done <<< "${CHANGED}"

if [[ "${CODE_CHANGED}" -eq 0 ]]; then
  echo "gate: no code changes; no evidence bundle required"
  exit 0
fi

echo "gate: code changes detected; requiring evidence bundle"

# Require at least one run folder containing plan + walkthrough.
if ! ls artifacts/history/runs/**/implementation_plan.json >/dev/null 2>&1; then
  echo "gate: ERROR: missing artifacts/history/runs/**/implementation_plan.json" >&2
  exit 1
fi
if ! ls artifacts/history/runs/**/walkthrough.md >/dev/null 2>&1; then
  echo "gate: ERROR: missing artifacts/history/runs/**/walkthrough.md" >&2
  exit 1
fi

for req in \
  "artifacts/logs/context_manifest.md" \
  "artifacts/logs/post_verify_report.md" \
  "artifacts/history/lessons-learned.md"
do
  [[ -f "${req}" ]] || { echo "gate: ERROR: missing ${req}" >&2; exit 1; }
done

echo "gate: OK"
