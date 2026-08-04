// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	gapicrypto "github.com/goppydae/gapi/core/crypto"
	gapiv1 "github.com/goppydae/gapi/pkg/proto"
	"google.golang.org/protobuf/proto"

	"github.com/goppydae/goblin/core/capability"
	"github.com/goppydae/goblin/core/migration"
)

// The goblin-ckpt plane (GOBLIN-DIV-031).
//
// Adding the ALPN to the registry only makes the listener ADVERTISE it.
// Without an adapter attached, negotiation succeeds and the router then
// refuses the connection with CodeALPNNotServing - which is what the
// two-node test hit: "ALPN goblin-ckpt not serving". The plane has to be
// registered and served, like every other.

// checkpointAuthorizer verifies the capability token a destination
// presents when pulling an instance's memory image.
//
// This is the only authorization on that transfer, and what it protects
// is the entire address space of a running process. It fails closed on
// every path: no token, unverifiable token, wrong right, or a token
// minted for a different subject.
func checkpointAuthorizer(resolver gapicrypto.KeyResolver, revocations *capability.Revocations,
	log *slog.Logger) migration.Authorizer {
	return func(token, instanceUUID []byte) error {
		if len(token) == 0 {
			return fmt.Errorf("checkpoint fetch: no capability token presented")
		}
		right, err := capability.RightForVerb(capability.VerbJobMigrate)
		if err != nil {
			return err
		}

		var tok gapiv1.CapabilityToken
		if err := proto.Unmarshal(token, &tok); err != nil {
			return fmt.Errorf("checkpoint fetch: undecodable capability token: %w", err)
		}
		payload, err := gapicrypto.VerifyCapabilityToken(&tok, resolver, time.Now(), right)
		if err != nil {
			return fmt.Errorf("checkpoint fetch: %w", err)
		}

		// Revocation, checked HERE because this is the only path where a
		// capability token crosses a trust boundary: the token arrives
		// from the destination node, where signal_rpc and authorize both
		// mint one locally and inspect it a line later. A freshly minted
		// UUIDv7 can never be in the filter, so those two checks are
		// inert by construction and this one is not (GOBLIN-DIV-015).
		//
		// It runs before subject binding on purpose. A revoked token has
		// no business being considered for any subject, and ordering the
		// cheap categorical test first keeps the expensive comparison off
		// tokens already known bad.
		if revocations != nil && revocations.IsRevoked(payload.GetTokenId()) {
			return fmt.Errorf("checkpoint fetch: capability token %x is revoked", payload.GetTokenId())
		}

		// Subject binding. Without this a token legitimately issued to
		// migrate one instance would read out any other instance's
		// memory, which is the whole reason tokens are scoped.
		if !bytes.Equal(payload.GetSubjectUuid(), instanceUUID) {
			return fmt.Errorf("checkpoint fetch: token subject does not match the requested instance")
		}

		log.LogAttrs(context.Background(), slog.LevelInfo, "checkpoint fetch authorized",
			slog.String("token_id", fmt.Sprintf("%x", payload.GetTokenId())))
		return nil
	}
}
