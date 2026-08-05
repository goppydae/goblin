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
	"strings"
)

// Version resolves the build version by, in order: explicit environment
// override (RELEASE_VERSION), the release tag when building from a tag ref
// (GITHUB_REF_NAME, only when GITHUB_REF_TYPE=tag), the root VERSION file,
// and finally "dev". The VERSION file is the single source of version truth
// in the repo; the earlier steps exist for release automation.
func Version() string {
	if v := os.Getenv("RELEASE_VERSION"); v != "" {
		return v
	}
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if v := os.Getenv("GITHUB_REF_NAME"); v != "" {
			return v
		}
	}
	if data, err := os.ReadFile("VERSION"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	return "dev"
}

// VersionMismatchError reports that the VERSION file and the tag being
// cut name different releases. Both values travel on the error so a
// caller can report the difference without parsing a message.
type VersionMismatchError struct {
	// File is the trimmed contents of the root VERSION file.
	File string
	// Tag is the tag ref name with one leading "v" removed.
	Tag string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("VERSION file reads %q but the tag being cut is %q; bump VERSION and re-cut the tag", e.File, e.Tag)
}

// CheckVersionAgainstTag fails when the root VERSION file disagrees with
// the tag ref being built. It is the reader that makes VERSION an
// invariant rather than a comment: the file is documented above and in
// the build-environment design as the single source of version truth,
// and until this existed nothing compared it to anything. It went stale
// three times in four days, the third time hours after the divergence
// entry was filed. See MAGELIB-DIV-006.
//
// It reads the file DIRECTLY and never through Version(). Version()
// prefers RELEASE_VERSION and then GITHUB_REF_NAME, so a check written
// in terms of it compares the tag to itself and passes unconditionally
// on exactly the builds this gate exists to police - it would measure
// the emitter instead of the property. That precedence order is also why
// the drift hides in the first place: a tag build reports the right
// string while the file is wrong.
//
// Off a tag ref it returns an error rather than nil. A check that
// silently succeeds when it cannot run reports success for its entire
// existence and is indistinguishable from one that works, so this has
// exactly one caller - each repo's release-guard workflow - and is
// deliberately not a Lint dependency.
//
// Comparison is exact string equality, not semver. The silo's tags carry
// suffixes like -proto2f, and a parser would introduce a normalisation
// step in which two spellings could compare equal - the opposite of what
// a drift gate wants.
func CheckVersionAgainstTag() error {
	if refType := os.Getenv("GITHUB_REF_TYPE"); refType != "tag" {
		return fmt.Errorf("GITHUB_REF_TYPE is %q, not \"tag\": this gate compares VERSION against a tag ref and has nothing to compare against here", refType)
	}
	refName := os.Getenv("GITHUB_REF_NAME")
	if refName == "" {
		return fmt.Errorf("GITHUB_REF_TYPE is \"tag\" but GITHUB_REF_NAME is empty")
	}
	tag := strings.TrimPrefix(refName, "v")

	data, err := os.ReadFile("VERSION")
	if err != nil {
		return fmt.Errorf("reading the VERSION file (this gate runs from the repo root): %w", err)
	}
	file := strings.TrimSpace(string(data))
	if file == "" {
		return fmt.Errorf("the VERSION file is empty; it must name the release being tagged (%s)", tag)
	}

	if file != tag {
		return &VersionMismatchError{File: file, Tag: tag}
	}
	fmt.Printf("VERSION agrees with the tag being cut: %s\n", file)
	return nil
}
