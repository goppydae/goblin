// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package ident mints the UUIDv7 identifiers used for kernel events,
// lifecycle runs, and capability tokens (operator decision 2026-07-28:
// all ids are UUIDv7 where reasonable). UUIDv7 is time-ordered, so ids
// sort by mint time - the property that makes them usable as foreign
// keys across logs, metrics, and traces (DDR-1/DDR-3).
package ident

import (
	"fmt"

	"github.com/google/uuid"
)

// NewV7 mints a UUIDv7 as raw bytes (the wire form).
func NewV7() []byte {
	u, err := uuid.NewV7()
	if err != nil {
		// NewV7 fails only if the entropy source does; identity minting
		// without entropy is not a state to limp through (uuid.New()
		// panics on the same condition).
		panic(fmt.Sprintf("ident: minting UUIDv7: %v", err))
	}
	b := [16]byte(u)
	return b[:]
}

// NewV7String mints a UUIDv7 in canonical string form (the log/CLI
// form).
func NewV7String() string {
	u, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("ident: minting UUIDv7: %v", err))
	}
	return u.String()
}
