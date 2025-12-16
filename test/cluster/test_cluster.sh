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
    -p 8081:8080 -p 7941:7946 \
    $IMAGE_TAG \
    /bin/goblind --id=node-1 --bind=0.0.0.0 --advertise=node-1:7946 --api-addr=0.0.0.0:8080 --bootstrap

NODE1_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' goblin-node-1)
log "Node 1 IP: $NODE1_IP"

# Wait for leader
log "Waiting for Node 1 to become leader..."
sleep 5

# 3. Start Node 2 (Joiner)
log "Starting Node 2..."
docker run -d --name goblin-node-2 \
    --hostname node-2 \
    -p 8082:8080 -p 7942:7946 \
    $IMAGE_TAG \
    /bin/goblind --id=node-2 --bind=0.0.0.0 --advertise=node-2:7946 --api-addr=0.0.0.0:8080 --join=$NODE1_IP:7946

# 4. Start Node 3 (Joiner)
log "Starting Node 3..."
docker run -d --name goblin-node-3 \
    --hostname node-3 \
    -p 8083:8080 -p 7943:7946 \
    $IMAGE_TAG \
    /bin/goblind --id=node-3 --bind=0.0.0.0 --advertise=node-3:7946 --api-addr=0.0.0.0:8080 --join=$NODE1_IP:7946

# 5. Verify Cluster Formation
log "Verifying cluster membership..."
sleep 5
MEMBERS=$(curl -s http://localhost:8081/v1/cluster/members | jq '.members | length')

if [ "$MEMBERS" -eq 3 ]; then
    log "SUCCESS: Cluster has 3 members."
else
    error "Cluster has $MEMBERS members, expected 3."
fi

# 6. Submit Job
log "Submitting test job..."
# TODO: Use goblinctl once built, or curl for now
# Assuming we have a SubmitJob endpoint. 
# For now, just verifying cluster formation is a HUGE win.

log "Integration test passed!"
