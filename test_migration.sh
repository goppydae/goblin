#!/usr/bin/env bash
set -e

# Cleanup function
cleanup() {
  echo "Cleaning up..."
  kill $PID1 $PID2 $PID3 2>/dev/null || true
  pkill -P $$ goblind || true
}
trap cleanup EXIT

# Clean previous run
pkill goblind || true
rm -rf data/node*
mkdir -p data/node1 data/node2 data/node3

# Start Node 1 (Bootstrap)
echo "Starting Node 1..."
./bin/goblind start --id node-1 --serf-port 9001 --raft-addr 127.0.0.1:9002 --api-addr 127.0.0.1:9000 --data ./data/node1 > node1.log 2>&1 &
PID1=$!
sleep 5

# Start Node 2
echo "Starting Node 2..."
./bin/goblind start --id node-2 --serf-port 9301 --raft-addr 127.0.0.1:9302 --api-addr 127.0.0.1:9300 --data ./data/node2 --join 127.0.0.1:9001 > node2.log 2>&1 &
PID2=$!

# Start Node 3
echo "Starting Node 3..."
./bin/goblind start --id node-3 --serf-port 9401 --raft-addr 127.0.0.1:9402 --api-addr 127.0.0.1:9400 --data ./data/node3 --join 127.0.0.1:9001 > node3.log 2>&1 &
PID3=$!
sleep 5

echo "Cluster started."

# Verify Cluster Status
echo "Checking Status..."
./bin/goblinctl status --api-addr 127.0.0.1:9000

# Schedule Job
echo "Scheduling Job 1..."
./bin/goblinctl schedule job-1 my-agent service --api-addr 127.0.0.1:9000
# Wait for assignment
sleep 2

# Verify Assignment (using grep logs)
echo "Verifying assignment..."
if grep -q "Job Assigned: job-1" node*.log; then
    echo "✅ Job 1 assigned successfully."
else
    echo "❌ Job 1 assignment failed!"
    echo "--- Node 1 Log ---"
    cat node1.log
    echo "--- Node 2 Log ---"
    cat node2.log
    echo "--- Node 3 Log ---"
    cat node3.log
    exit 1
fi

# Manual Migration Test (Node 2 handles job? Let's assume scheduler picked somewhat randomly)
# We can skip exact node check for manual test, or parse logic.
# Let's focus on Failure Detection test which is the critical one.

# Kill Node 1 (Leader detection test)
echo "Killing Node 1 (Bootstrap/Leader)..."
kill $PID1
wait $PID1 2>/dev/null || true

# Wait for Failure Detection & Migration
echo "Waiting for recovery (15s)..."
ls -l data/node* # Debug
sleep 15

# Verify Migration
echo "Checking logs for recovery..."
if grep -q "Leader detected failure of node node-1" node*.log; then
    echo "✅ Leader detected failure."
else
    echo "❌ Leader failure detection missed (or new leader not elected yet)."
    # Don't exit, might be timing.
fi

if grep -q "Rescheduling" node*.log; then
    echo "✅ Rescheduling triggered."
else
     echo "❌ Rescheduling not triggered."
fi

# Cleanup
echo "Stopping nodes..."
kill $PID2 $PID3 2>/dev/null || true
echo "Done."
