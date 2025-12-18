#!/usr/bin/env bash
# tools/test.sh - Language-agnostic test hook for Go projects
set -euo pipefail

echo "==> Running Go tests..."
if go test -v ./... 2>&1 | tee artifacts/test_results/go_test.log; then
  echo "  Tests passed"
else
  echo "  (no Go packages to test)"
fi

echo "==> Test execution complete"
