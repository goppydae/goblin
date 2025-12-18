#!/usr/bin/env bash
# tools/verify_go.sh - Go-specific verification
set -euo pipefail

# Find all directories containing go.mod (excluding vendor and build contexts)
MOD_DIRS=$(find . -name "go.mod" -not -path "./vendor/*" -not -path "*/scenarios/*/build_context/*" -exec dirname {} \;)

if [ -z "$MOD_DIRS" ]; then
    echo "No Go modules found; skipping Go verification."
    exit 0
fi

# Store the top-level directory for absolute path log logic
TOP_DIR=$(git rev-parse --show-toplevel)

for dir in $MOD_DIRS; do
    echo "==> Verifying Go module in $dir..."
    (
        cd "$dir"
        echo "  Running go vet..."
        # We manually check packages to avoid "matched no packages" warnings failing set -e
        PACKAGES=$(go list ./... 2>/dev/null || true)
        if [ -n "$PACKAGES" ]; then
            go vet ./... 2>&1 | tee -a "$TOP_DIR/artifacts/logs/go_vet.log"
        else
            echo "  (no Go packages in $dir)"
        fi
        
        echo "  Checking go fmt..."
        # Exclude vendor and build contexts from formatting check
        UNFORMATTED=$(gofmt -l . 2>&1 | grep -v '^vendor/' | grep -v 'build_context/' || true)
        if [ -n "$UNFORMATTED" ]; then
            echo "ERROR: The following files in $dir need formatting:"
            echo "$UNFORMATTED"
            exit 1
        fi
        
        echo "  Checking go mod tidy..."
        go mod tidy
        if ! git diff --exit-code go.mod go.sum; then
            echo "ERROR: go.mod or go.sum in $dir needs tidying"
            exit 1
        fi
    )
done

echo "==> Go verification complete"
