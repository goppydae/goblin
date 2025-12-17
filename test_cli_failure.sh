#!/bin/bash
set -e

# Build binaries
CGO_ENABLED=0 go build -o goblind cmd/goblind/main.go
CGO_ENABLED=0 go build -o goblinctl cmd/goblinctl/main.go

# Cleanup
pkill -f goblind || true
rm -rf /tmp/goblin-cli-test
mkdir -p /tmp/goblin-cli-test

# Generate certs (hardcoded path in gen_certs.go)
go run test/cluster/gen_certs.go

echo "Inspecting CA certificate:"
openssl x509 -in /tmp/goblin-test-certs/ca.crt -noout -dates -subject
echo "Inspecting Node-1 certificate:"
openssl x509 -in /tmp/goblin-test-certs/node-1.crt -noout -dates -subject

# Start one node (rename node specific cert if needed or just use node-1 certs)
./goblind \
    --id node-1 \
    --serf-addr 127.0.0.1:29111 \
    --raft-addr 127.0.0.1:29121 \
    --api-addr 127.0.0.1:29005 \
    --data /tmp/goblin-cli-test/data \
    --cert /tmp/goblin-test-certs/node-1.crt \
    --key /tmp/goblin-test-certs/node-1.key \
    --ca /tmp/goblin-test-certs/ca.crt &

PID=$!
sleep 5

# Test Status (Should work)
echo "Testing goblinctl status..."
./goblinctl status --api-addr 127.0.0.1:29005 --tls-ca /tmp/goblin-test-certs/ca.crt

# Test Publish (Expected to fail if interface mismatch prevents casting)
echo "Testing goblinctl publish..."
OUTPUT=$(./goblinctl publish test-event "hello world" --api-addr 127.0.0.1:29005 --tls-ca /tmp/goblin-test-certs/ca.crt)
echo "$OUTPUT"

if [[ "$OUTPUT" == *"membership does not support UserEvent"* ]]; then
    echo "❌ VERIFICATION FAILED: Membership interface mismatch detected!"
    kill $PID
    exit 1
fi

if [[ "$OUTPUT" == *"event 'test-event' published"* ]]; then
    echo "✅ VERIFICATION PASSED: Event published."
else
    echo "❌ VERIFICATION FAILED: Unexpected output"
    kill $PID
    exit 1
fi

kill $PID
