// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package crypto

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	gapiv1 "github.com/goppydae/gapi/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// Rights bits carried in a capability token's bitmap. The kernel checks
// these at delivery; the orchestrator checks them at the FSM before a
// signal is even committed (defense in depth).
const (
	// RightSignalTerm authorizes graceful-stop signals (SIGTERM, SIGINT).
	RightSignalTerm uint64 = 1 << 0
	// RightSignalKill authorizes SIGKILL.
	RightSignalKill uint64 = 1 << 1
	// RightSignalUser authorizes SIGUSR1/SIGUSR2.
	RightSignalUser uint64 = 1 << 2
)

// Typed verification failures. Errors are data: callers branch on these,
// never on message strings.
var (
	ErrTokenMalformed  = errors.New("capability token: malformed")
	ErrTokenUnknownKey = errors.New("capability token: unknown signing key")
	ErrTokenSignature  = errors.New("capability token: signature verification failed")
	ErrTokenExpired    = errors.New("capability token: expired")
	ErrTokenRights     = errors.New("capability token: insufficient rights")
)

// KeyResolver maps a token's key_id to the issuer public key. Returning
// false fails the token closed with ErrTokenUnknownKey.
type KeyResolver func(keyID string) (ed25519.PublicKey, bool)

// RightForSignal maps a signal number to the capability right that
// authorizes it. Signals with no mapping (SIGSEGV, SIGSTOP, ...) are
// never grantable and fail closed.
func RightForSignal(signum int32) (uint64, error) {
	switch signum {
	case 2, 15: // SIGINT, SIGTERM
		return RightSignalTerm, nil
	case 9: // SIGKILL
		return RightSignalKill, nil
	case 10, 12: // SIGUSR1, SIGUSR2
		return RightSignalUser, nil
	default:
		return 0, fmt.Errorf("%w: signal %d is not grantable", ErrTokenRights, signum)
	}
}

// SignCapabilityToken serializes payload and signs the literal bytes.
// The signature covers exactly the bytes embedded in the token -
// protobuf serialization is not canonical, so verifiers must never
// re-serialize before checking.
func SignCapabilityToken(payload *gapiv1.CapabilityTokenPayload, priv ed25519.PrivateKey) (*gapiv1.CapabilityToken, error) {
	if payload == nil {
		return nil, fmt.Errorf("%w: nil payload", ErrTokenMalformed)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing capability token: private key size %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	raw, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("signing capability token: marshal payload: %w", err)
	}
	return &gapiv1.CapabilityToken{
		Payload:   raw,
		Signature: ed25519.Sign(priv, raw),
	}, nil
}

// VerifyCapabilityToken is the kernel's single verification codepath
// (GAPI-DIV-017). It checks structure, signature (over the literal
// payload bytes), validity window, and rights, in that order, and
// returns the verified payload. The payload is parsed before the
// signature check only to learn key_id; no other field is trusted
// until the signature has passed.
func VerifyCapabilityToken(tok *gapiv1.CapabilityToken, resolve KeyResolver, now time.Time, requiredRights uint64) (*gapiv1.CapabilityTokenPayload, error) {
	if tok == nil || len(tok.Payload) == 0 || len(tok.Signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: missing payload or signature", ErrTokenMalformed)
	}
	if resolve == nil {
		return nil, fmt.Errorf("%w: no key resolver configured", ErrTokenUnknownKey)
	}

	var payload gapiv1.CapabilityTokenPayload
	if err := proto.Unmarshal(tok.Payload, &payload); err != nil {
		return nil, fmt.Errorf("%w: undecodable payload: %w", ErrTokenMalformed, err)
	}

	pub, ok := resolve(payload.KeyId)
	if !ok {
		return nil, fmt.Errorf("%w: key id %q", ErrTokenUnknownKey, payload.KeyId)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: resolved key size %d, want %d", ErrTokenUnknownKey, len(pub), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(pub, tok.Payload, tok.Signature) {
		return nil, ErrTokenSignature
	}

	// Signature verified: fields are trustworthy from here on.
	if payload.ExpiresAtMs <= payload.IssuedAtMs {
		return nil, fmt.Errorf("%w: expires_at_ms %d not after issued_at_ms %d", ErrTokenMalformed, payload.ExpiresAtMs, payload.IssuedAtMs)
	}
	if now.UnixMilli() >= payload.ExpiresAtMs {
		return nil, fmt.Errorf("%w: at %d, expired %d", ErrTokenExpired, now.UnixMilli(), payload.ExpiresAtMs)
	}
	if missing := requiredRights &^ payload.Rights; missing != 0 {
		return nil, fmt.Errorf("%w: missing rights bitmap %#x", ErrTokenRights, missing)
	}

	return &payload, nil
}
