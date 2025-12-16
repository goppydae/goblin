#!/usr/bin/env bash
set -e

# Cleanup on exit
cleanup() {
  echo "Cleaning up..."
  kill $GOBLIN_PID 2>/dev/null || true
  rm -rf /tmp/goblin-test
}
trap cleanup EXIT

echo "Building binaries..."
# Assume binaries are already built in bin/

# Find project root (one level up from this script, assuming script is in test/)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
# Or assuming run from root, but let's be robust:
BIN_DIR="$PROJECT_ROOT/bin"

echo "Using binaries in $BIN_DIR"

echo "Starting Goblin node..."
mkdir -p /tmp/goblin-test
# gapi start uses --id, --api-addr, --gossip-addr typically. Checking common pattern.
"$BIN_DIR/goblind" start --id node1 --data /tmp/goblin-test/node1 --api-addr :8080 --serf-addr 127.0.0.1 --serf-port 9191 --runtime-addr 127.0.0.1:4243 &
GOBLIN_PID=$!

# Wait for startup
sleep 2

echo "Testing KV Store..."
"$BIN_DIR/goblinctl" --api-addr :8080 kv set foo bar
VAL=$("$BIN_DIR/goblinctl" --api-addr :8080 kv get foo)

if [ "$VAL" == "bar" ]; then
  echo "✅ KV Set/Get passed: foo=$VAL"
else
  echo "❌ KV Set/Get failed: expected bar, got $VAL"
  exit 1
fi

echo "✅ Cluster test passed!"
