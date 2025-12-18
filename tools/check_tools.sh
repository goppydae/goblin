#!/usr/bin/env bash
# Verify that required external tools are present in the environment.

set -o pipefail
set -o nounset

MISSING=0

check_tool() {
  local tool_name="$1"
  local install_hint="$2"

  if ! command -v "$tool_name" &> /dev/null; then
    echo "ERROR: '$tool_name' not found."
    echo "  -> $install_hint"
    MISSING=1
  else
    echo "OK: $tool_name ($(command -v "$tool_name"))"
  fi
}

echo "==> Checking development tools..."

# check_tool "mdformat" "Install via pip: pip install mdformat-gfm mdformat-frontmatter mdformat-footnote"
# mdformat is checked via python import in the runner script mostly, but good to have CLI too.
check_tool "mdformat" "Run 'nix develop' to enter the development environment"

check_tool "markdownlint-cli2" "Run 'nix develop' to enter the development environment"


if [ "$MISSING" -eq 1 ]; then
  echo ""
  echo "FAIL: Missing required tools. Please install them to proceed."
  exit 1
fi

echo "SUCCESS: All tools present."
exit 0
