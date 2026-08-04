// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package ident_test

import (
	"testing"

	"github.com/goppydae/goblin/internal/ident"
)

func TestNewV7_IsValidVersion7(t *testing.T) {
	b := ident.NewV7()
	if len(b) != 16 {
		t.Fatalf("NewV7 length = %d, want 16", len(b))
	}
	if version := b[6] >> 4; version != 7 {
		t.Fatalf("UUID version = %d, want 7", version)
	}
}

func TestNewV7_Monotonic(t *testing.T) {
	// UUIDv7 is time-ordered; ids minted in sequence must be distinct
	// and non-decreasing byte-wise (the property placement audit relies on).
	prev := ident.NewV7()
	for i := 0; i < 100; i++ {
		next := ident.NewV7()
		if string(next) == string(prev) {
			t.Fatalf("duplicate UUIDv7 minted at iteration %d", i)
		}
		if string(next) < string(prev) {
			t.Fatalf("UUIDv7 order regressed at iteration %d: %x < %x", i, next, prev)
		}
		prev = next
	}
}

func TestStringParse_RoundTrip(t *testing.T) {
	b := ident.NewV7()
	s := ident.String(b)
	if len(s) != 36 {
		t.Fatalf("canonical form length = %d, want 36", len(s))
	}
	back, err := ident.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	if string(back) != string(b) {
		t.Fatalf("round trip mismatch: %x != %x", back, b)
	}
}

func TestString_InvalidLengthEmpty(t *testing.T) {
	if got := ident.String([]byte{1, 2, 3}); got != "" {
		t.Fatalf("String(short) = %q, want empty", got)
	}
	if got := ident.String(nil); got != "" {
		t.Fatalf("String(nil) = %q, want empty", got)
	}
}

func TestParse_RejectsGarbage(t *testing.T) {
	if _, err := ident.Parse("not-a-uuid"); err == nil {
		t.Fatal("Parse accepted garbage")
	}
	if _, err := ident.Parse(""); err == nil {
		t.Fatal("Parse accepted empty string")
	}
}
