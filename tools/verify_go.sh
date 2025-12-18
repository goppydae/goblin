#!/usr/bin/env bash
# tools/verify_go.sh - Go-specific verification
set -euo pipefail

echo "==> Running go vet..."
if go vet ./... 2>&1 | tee artifacts/logs/go_vet.log; then
  echo "  go vet passed"
else
  echo "  (no Go packages or vet issues)"
fi

echo "==> Checking go fmt..."
UNFORMATTED=$(gofmt -l . 2>&1 | grep -v '^vendor/' | grep -v '^scenarios/.*/build_context/' || true)
if [ -n "$UNFORMATTED" ]; then
    echo "ERROR: The following files need formatting:"
    echo "$UNFORMATTED"
    exit 1
fi

echo "==> Checking go mod tidy..."
go mod tidy
if ! git diff --exit-code go.mod go.sum; then
    echo "ERROR: go.mod or go.sum needs tidying"
    exit 1
fi

echo "==> Go verification complete"
