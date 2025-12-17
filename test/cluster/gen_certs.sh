#!/usr/bin/env bash
set -e

# Directory to store certs
CERT_DIR="/tmp/goblin-test-certs"
rm -rf "$CERT_DIR"
mkdir -p "$CERT_DIR"

log() { echo -e "\033[0;32m[CERTS]\033[0m $1"; }

# 1. Generate CA
log "Generating CA..."
openssl genrsa -out "$CERT_DIR/ca.key" 2048
openssl req -new -x509 -days 365 -key "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" -subj "/CN=Goblin Test CA"

# Function to generate cert for a node
generate_node_cert() {
    NODE_ID=$1
    IP="127.0.0.1"
    
    log "Generating cert for $NODE_ID..."
    
    # Key
    openssl genrsa -out "$CERT_DIR/$NODE_ID.key" 2048
    
    # CSR
    openssl req -new -key "$CERT_DIR/$NODE_ID.key" -out "$CERT_DIR/$NODE_ID.csr" -subj "/CN=$NODE_ID"
    
    # Config for SAN (Subject Alternative Name)
    cat > "$CERT_DIR/$NODE_ID.ext" <<EOF
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = @alt_names

[alt_names]
DNS.1 = $NODE_ID
IP.1 = $IP
EOF

    # Sign
    openssl x509 -req -in "$CERT_DIR/$NODE_ID.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" \
        -CAcreateserial -out "$CERT_DIR/$NODE_ID.crt" -days 365 -sha256 -extfile "$CERT_DIR/$NODE_ID.ext"
}

# 2. Generate Certs for 3 nodes
generate_node_cert "node-1"
generate_node_cert "node-2"
generate_node_cert "node-3"

log "Certificates generated in $CERT_DIR"
ls -l "$CERT_DIR"
