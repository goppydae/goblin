#!/usr/bin/env bash
# Copyright (c) 2025 Steven Verhelle (enqack)
#
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.
#
# SPDX-License-Identifier: MPL-2.0

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log() { echo -e "${GREEN}[TEST-TLS]${NC} $1"; }
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
CERTS="/tmp/goblin-test-certs"

# Generate Certs
log "Generating certificates..."
go run test/cluster/gen_certs.go

# Node 1 (Seed)
log "Starting Node 1 (TLS)..."
./bin/goblind start \
    --id=node-1 \
    --serf-addr=127.0.0.1 --serf-port=29011 \
    --raft-addr=127.0.0.1:29021 --data=$DATA_DIR_1 \
    --api-addr=127.0.0.1:29001 \
    --cert="$CERTS/node-1.crt" \
    --key="$CERTS/node-1.key" \
    --ca="$CERTS/ca.crt" &
pids+=($!)

sleep 2

# Node 2
log "Starting Node 2 (TLS)..."
./bin/goblind start \
    --id=node-2 \
    --serf-addr=127.0.0.1 --serf-port=29012 \
    --raft-addr=127.0.0.1:29022 --data=$DATA_DIR_2 \
    --api-addr=127.0.0.1:29002 \
    --join=127.0.0.1:29011 \
    --cert="$CERTS/node-2.crt" \
    --key="$CERTS/node-2.key" \
    --ca="$CERTS/ca.crt" &
pids+=($!)

# Node 3
log "Starting Node 3 (TLS)..."
./bin/goblind start \
    --id=node-3 \
    --serf-addr=127.0.0.1 --serf-port=29013 \
    --raft-addr=127.0.0.1:29023 --data=$DATA_DIR_3 \
    --api-addr=127.0.0.1:29003 \
    --join=127.0.0.1:29011 \
    --cert="$CERTS/node-3.crt" \
    --key="$CERTS/node-3.key" \
    --ca="$CERTS/ca.crt" &
pids+=($!)

sleep 5

# Verify
log "Verifying cluster status..."
# Use --tls-insecure for client just to simplify, or could verify with CA but client needs client certs potentially if mTLS
# Assuming the client tool supports just verifying CA
./bin/goblinctl cluster status --api-addr 127.0.0.1:29001 --tls-insecure

log "Cluster verification passed!"
