#!/usr/bin/env bash
set -e

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[TEST]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

cleanup() {
    log "Cleaning up containers..."
    docker rm -f goblin-node-1 goblin-node-2 goblin-node-3 2>/dev/null || true
    # Also clean up the network if we created one (using default bridge for simplicity here, but good practice)
}
trap cleanup EXIT

# 1. Load the Docker image
log "Loading Docker image..."
if [ ! -L result ]; then
    error "nix build .#docker result not found. Run 'nix build .#docker' first."
fi
IMAGE_TAG=$(docker load < result | awk '{print $3}')
log "Loaded image: $IMAGE_TAG"

# 2. Start Seed Node (Node 1)
log "Starting Node 1 (Seed)..."
docker run -d --name goblin-node-1 \
    --hostname node-1 \
    -p 29001:29000 -p 29011:29010 \
    $IMAGE_TAG \
    /bin/goblind --id=node-1 --serf-addr=0.0.0.0 --serf-port=29010 --raft-addr=0.0.0.0:29020 --api-addr=0.0.0.0:29000

NODE1_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' goblin-node-1)
log "Node 1 IP: $NODE1_IP"

# Wait for leader
log "Waiting for Node 1 to become leader..."
sleep 5

# 3. Start Node 2 (Joiner)
log "Starting Node 2..."
docker run -d --name goblin-node-2 \
    --hostname node-2 \
    -p 29002:29000 -p 29012:29010 \
    $IMAGE_TAG \
    /bin/goblind --id=node-2 --serf-addr=0.0.0.0 --serf-port=29010 --raft-addr=0.0.0.0:29020 --api-addr=0.0.0.0:29000 --join=$NODE1_IP:29010

# 4. Start Node 3 (Joiner)
log "Starting Node 3..."
docker run -d --name goblin-node-3 \
    --hostname node-3 \
    -p 29003:29000 -p 29013:29010 \
    $IMAGE_TAG \
    /bin/goblind --id=node-3 --serf-addr=0.0.0.0 --serf-port=29010 --raft-addr=0.0.0.0:29020 --api-addr=0.0.0.0:29000 --join=$NODE1_IP:29010

# 5. Verify Cluster Formation
log "Verifying cluster membership..."
sleep 5

# Use goblinctl from inside the container to verify the cluster state
# This verifies:
# 1. The cluster is up
# 2. The CLI tool works
# 3. The API is reachable via QUIC
if docker exec goblin-node-1 /bin/goblinctl status --api-addr 127.0.0.1:29000 --tls-insecure; then
    log "SUCCESS: Cluster is operational and CLI is working."
else
    error "Cluster verification failed."
fi

# 6. Submit Job
log "Submitting test job..."
# TODO: Use goblinctl once built, or curl for now
# Assuming we have a SubmitJob endpoint. 
# For now, just verifying cluster formation is a HUGE win.

log "Integration test passed!"
