// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package capability is the orchestrator half of the capability-token
// scheme (GOBLIN-DIV-015, DDR-9): Ed25519 bearer-token issuance with a
// clamped TTL, and a revocation Bloom filter that rides gossip. The
// kernel half - the single verification codepath - lives in gapi
// core/crypto.
package capability

import (
	"crypto/ed25519"
	"fmt"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapiv1 "github.com/goppydae/gapi/pkg/proto"
	"github.com/goppydae/goblin/internal/ident"
)

// TTL policy (operator decision 2026-07-28): 120s default, clamped to
// the 60-300s range from the proto-2 plan.
const (
	TTLDefault = 120 * time.Second
	TTLMin     = 60 * time.Second
	TTLMax     = 300 * time.Second
)

// Issuer mints capability tokens under a per-boot Ed25519 keypair. The
// public key travels in the node's serf tags (gossip), so any node can
// resolve key_id -> key without a Raft round-trip.
type Issuer struct {
	nodeID string
	keyID  string
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	now    func() time.Time
}

// NewIssuer generates a fresh keypair; the key id is a UUIDv7, so key
// generations sort by boot time.
func NewIssuer(nodeID string) (*Issuer, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("capability issuer keygen: %w", err)
	}
	return &Issuer{
		nodeID: nodeID,
		keyID:  ident.String(ident.NewV7()),
		priv:   priv,
		pub:    pub,
		now:    time.Now,
	}, nil
}

// KeyID identifies this issuer's signing key.
func (i *Issuer) KeyID() string { return i.keyID }

// PublicKey is the verification key matching KeyID.
func (i *Issuer) PublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), i.pub...)
}

// Issue mints a bearer token for one subject with the given rights. The
// subject is usually an agent instance, but orchestration tokens scope
// to a named resource instead - spec, node, job, or topic
// (GOBLIN-DIV-027) - which is why it is not called an instance UUID.
// ttl 0 means the 120s default; anything outside 60-300s is clamped.
// Returns the token and its raw UUIDv7 token id (the revocation
// handle).
func (i *Issuer) Issue(subjectUUID []byte, rights uint64, ttl time.Duration) (*gapiv1.CapabilityToken, []byte, error) {
	if len(subjectUUID) != 16 {
		return nil, nil, fmt.Errorf("capability issue: subject UUID must be 16 bytes, got %d", len(subjectUUID))
	}
	switch {
	case ttl == 0:
		ttl = TTLDefault
	case ttl < TTLMin:
		ttl = TTLMin
	case ttl > TTLMax:
		ttl = TTLMax
	}

	tokenID := ident.NewV7()
	issued := i.now()
	payload := &gapiv1.CapabilityTokenPayload{
		TokenId:      tokenID,
		SubjectUuid:  append([]byte(nil), subjectUUID...),
		Rights:       rights,
		IssuedAtMs:   issued.UnixMilli(),
		ExpiresAtMs:  issued.Add(ttl).UnixMilli(),
		IssuerNodeId: i.nodeID,
		KeyId:        i.keyID,
	}
	tok, err := gapicrypto.SignCapabilityToken(payload, i.priv)
	if err != nil {
		return nil, nil, err
	}
	return tok, tokenID, nil
}
