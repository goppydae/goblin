// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"fmt"
	"os"

	"github.com/magefile/mage/sh"
)

// BufGenerate is the schema gate: buf generate, then buf lint, then buf
// breaking against the given git input (e.g. ".git#ref=HEAD"). Routing every
// repo's Proto target through here keeps proto gating in one place
// (schema governance: lint catches package drift at generation time,
// breaking makes an incompatible edit a red build).
//
// PROTO_BREAKING_SKIP=1 skips the breaking step, loudly. Its one
// legitimate use is an intentional schema reset (an operator-approved
// wire break, pre-1.0): the working tree cannot pass a comparison
// against the pre-reset baseline by design. The first post-commit run
// must be made WITHOUT the variable so the gate re-arms against the
// new baseline.
//
// PROTO_BREAKING_AGAINST overrides the caller's baseline. The callers
// pass ".git#ref=HEAD", which is right for a developer comparing an
// edited working tree against the last commit, and useless anywhere the
// working tree IS the commit: a fresh CI checkout compares the schema
// against itself and cannot fail. CI must therefore name a baseline it
// does not already equal (the pull request's merge base, or HEAD~1 on a
// push). The chosen baseline is echoed before the run so a build log
// records what the gate actually compared.
func BufGenerate(against string) error {
	fmt.Println("Generating protobuf code (buf)...")
	if err := sh.RunV("buf", "generate"); err != nil {
		return fmt.Errorf("buf generate: %w", err)
	}
	fmt.Println("Linting protobuf definitions (buf lint)...")
	if err := sh.RunV("buf", "lint"); err != nil {
		return fmt.Errorf("buf lint: %w", err)
	}
	baseline, skip := breakingPlan(against)
	if skip {
		fmt.Println("BREAKING: SKIPPED (PROTO_BREAKING_SKIP=1 - intentional schema reset; re-run without it after commit)")
		return nil
	}
	fmt.Printf("Checking for breaking schema changes (buf breaking --against %s)...\n", baseline)
	if err := sh.RunV("buf", "breaking", "--against", baseline); err != nil {
		return fmt.Errorf("buf breaking: %w", err)
	}
	return nil
}

// breakingPlan decides what the breaking check compares against, and
// whether it runs at all. It is split out of BufGenerate because
// BufGenerate shells out to buf and cannot be unit tested, while this
// decision is the part that has actually been wrong before: it is pure
// apart from the two environment variables it reads, so it is testable.
//
// Precedence: PROTO_BREAKING_SKIP=1 wins over everything, including an
// explicit PROTO_BREAKING_AGAINST. The skip is an operator escape hatch
// for an intentional schema reset, so a baseline configured elsewhere
// (a CI variable, a shell export) must not silently re-arm the gate.
func breakingPlan(against string) (baseline string, skip bool) {
	if os.Getenv("PROTO_BREAKING_SKIP") == "1" {
		return "", true
	}
	if override := os.Getenv("PROTO_BREAKING_AGAINST"); override != "" {
		return override, false
	}
	return against, false
}

// MirrorGenerated and the protoc-based GenerateProto are gone: both
// repos generate directly into their canonical import directory via
// buf.gen.yaml's module opt, so there is no mirror step to run.
