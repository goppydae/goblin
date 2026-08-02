#!/usr/bin/env bash
set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log() { echo -e "${GREEN}[TEST]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# Cleanup on exit
pids=()
cleanup() {
    log "Cleaning up processes..."
    for pid in "${pids[@]}"; do
        kill $pid 2>/dev/null || true
    done
    rm -rf /tmp/goblin-test-*
}
trap cleanup EXIT

# Build binaries
log "Building binaries..."
go build -mod=vendor -o bin/goblind ./cmd/goblind
go build -mod=vendor -o bin/goblinctl ./cmd/goblinctl

# Directories
DATA_DIR_1=$(mktemp -d /tmp/goblin-test-1-XXXXXX)
DATA_DIR_2=$(mktemp -d /tmp/goblin-test-2-XXXXXX)
DATA_DIR_3=$(mktemp -d /tmp/goblin-test-3-XXXXXX)

# Node 1 (Seed)
log "Starting Node 1..."
./bin/goblind start \
    --id=node-1 \
    --serf-addr=127.0.0.1 --serf-port=29011 \
    --raft-addr=127.0.0.1:29021 --data=$DATA_DIR_1 \
    --api-addr=127.0.0.1:29001 &
pids+=($!)

sleep 2 # Wait for leader election

# Node 2
log "Starting Node 2..."
./bin/goblind start \
    --id=node-2 \
    --serf-addr=127.0.0.1 --serf-port=29012 \
    --raft-addr=127.0.0.1:29022 --data=$DATA_DIR_2 \
    --api-addr=127.0.0.1:29002 \
    --join=127.0.0.1:29011 &
pids+=($!)

# Node 3
log "Starting Node 3..."
./bin/goblind start \
    --id=node-3 \
    --serf-addr=127.0.0.1 --serf-port=29013 \
    --raft-addr=127.0.0.1:29023 --data=$DATA_DIR_3 \
    --api-addr=127.0.0.1:29003 \
    --join=127.0.0.1:29011 &
pids+=($!)

sleep 5 # Wait for cluster formation

# Verify
log "Verifying cluster status..."
./bin/goblinctl cluster status --api-addr 127.0.0.1:29001 --tls-insecure

# Check for successful output (command exit code check is implicit with set -e)
log "Cluster verification passed!"
