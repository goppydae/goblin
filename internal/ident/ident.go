// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package ident mints and formats the 16-byte UUIDv7 identifiers used
// for specs, instances, and tokens (operator decision 2026-07-28: all
// ids are UUIDv7 where reasonable; bytes on the wire, canonical string
// at CLI/log boundaries).
package ident

import (
	"fmt"

	"github.com/google/uuid"
)

// NewV7 mints a UUIDv7 as raw bytes. UUIDv7 is time-ordered, so ids
// sort by mint time - a property audit trails rely on.
func NewV7() []byte {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the entropy source does; identity minting
		// without entropy is not a state to limp through.
		panic(fmt.Sprintf("ident: minting UUIDv7: %v", err))
	}
	b := [16]byte(u)
	return b[:]
}

// String renders 16 raw bytes in canonical UUID form. Anything that is
// not exactly 16 bytes renders empty - callers treat that as absent.
func String(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	u, err := uuid.FromBytes(b)
	if err != nil {
		return ""
	}
	return u.String()
}

// Parse converts a canonical UUID string back to 16 raw bytes.
func Parse(s string) ([]byte, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("ident: parsing %q: %w", s, err)
	}
	b := [16]byte(u)
	return b[:], nil
}
