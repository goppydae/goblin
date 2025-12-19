#!/usr/bin/env bash
set -euo pipefail

# clean.sh - Removes build artifacts, caches, and temporary files.

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

echo "Cleaning up Python caches..."
find . -type d -name "__pycache__" -exec rm -rf {} +
find . -type d -name ".pytest_cache" -exec rm -rf {} +
find . -type f -name "*.pyc" -delete

echo "Cleaning up coverage reports..."
find . -type f -name ".coverage" -delete
find . -type d -name "htmlcov" -exec rm -rf {} +

echo "Cleaning up test artifacts..."
# Clean contents but keep the directory structure if possible, or just ignore if it recreates.
# We will be conservative and only delete files inside artifacts/test_results
if [ -d "artifacts/test_results" ]; then
    find artifacts/test_results -mindepth 1 -delete
fi

echo "Done."
