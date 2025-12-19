#!/usr/bin/env bash
set -euo pipefail

NAME="${1:-repo}"
zip -r "${NAME}.zip" . \
  -x ".git/*" ".venv/*" "venv/*" "__pycache__/*" \
  ".pytest_cache/*" "artifacts/*"
