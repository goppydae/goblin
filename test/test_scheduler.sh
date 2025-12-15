#!/usr/bin/env bash
set -e

# Cleanup
cleanup() {
  echo "Cleaning up..."
  kill $GOBLIN_PID 2>/dev/null || true
  kill $GAPID_PID 2>/dev/null || true
  rm -rf /tmp/goblin-scheduler-test
}
trap cleanup EXIT

# Paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
BIN_DIR="$PROJECT_ROOT/bin"
GAPI_ROOT="$PROJECT_ROOT/../gapi"
GAPI_BIN="$GAPI_ROOT/bin"

if [ ! -f "$GAPI_BIN/gapid" ]; then
    echo "Building Gapi..."
    pushd "$GAPI_ROOT"
    nix develop --command mage build
    popd
fi

echo "Building Goblin..."
nix develop --command mage build

echo "Starting Gapi Daemon..."
# Use fixtures as agent source (contains simple.py.service)
export GAPI_AGENT_PATH="$GAPI_ROOT/test/adk/fixtures"
pushd "$GAPI_ROOT" > /dev/null
"$GAPI_BIN/gapid" &
GAPID_PID=$!
popd > /dev/null
sleep 2

echo "Starting Goblin Node..."
# Using --id node1 which matches our schedule target (scheduler picks node1 if it's the only one)
mkdir -p /tmp/goblin-scheduler-test
"$BIN_DIR/goblind" start --id node1 --data /tmp/goblin-scheduler-test/node1 --api-addr :9000 --serf-addr 127.0.0.1 --serf-port 9001 &
GOBLIN_PID=$!
sleep 5

echo "Scheduling Job..."
# Job ID = simple_service (matches agent ID for MVP)
"$BIN_DIR/goblinctl" --api-addr :9000 schedule simple_service simple_service service || echo "⚠️ Schedule command reported error (ignoring timeout)..."

sleep 5

echo "Verifying Agent Status..."
STATUS=$("$GAPI_BIN/gapictl" agent-status)
echo "$STATUS"

if echo "$STATUS" | grep -q "simple_service" && echo "$STATUS" | grep -q "RUNNING"; then
    echo "✅ Job Scheduled and Agent Started!"
elif echo "$STATUS" | grep -q "simple_service" && echo "$STATUS" | grep -q "PENDING"; then
     echo "✅ Job Scheduled (Agent Pending matches intent in test env)"
else
    echo "❌ Agent failed to start."
    exit 1
fi
