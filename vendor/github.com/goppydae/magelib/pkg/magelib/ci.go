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

	"github.com/magefile/mage/sh"
)

// Local reproduction of a repository's pull-request workflow.
//
// THE PROBLEM THIS SOLVES. Every step in every repo's ci.yml is already
// a local command - `nix develop -c mage <target>`, `nix build`, or
// `nix flake check`. Nothing in CI is GitHub-specific. But no target
// named the UNION, so "did I break CI?" meant reading six YAML files by
// hand, and an audit found each repo's `All` covering two of sixteen.
//
// THE DISTINCTION THAT MAKES THIS A DIFFERENT TARGET FROM `All`. `All`
// runs Fmt and Tidy, which REPAIR the tree. CI never repairs; it checks,
// through lint's gofmt check and through regenerate-then-assert-clean.
// So `All` CANNOT FAIL THE WAY CI FAILS - an unformatted file is fixed
// by `All` and rejected by CI. A target that repairs what CI checks is a
// different object from one that verifies it, and conflating them is how
// a green local run stops predicting a green remote one.
//
// WHAT IS DELIBERATELY NOT RUN IS PRINTED, NOT OMITTED. A local run is
// always a subset - jobs needing a scratch clone, a network token, or a
// booted guest do not belong in an inner loop. `Excluded` names them and
// RunCI prints them on success, because a shorter run that reports the
// same green as a longer one is the failure mode this repo files entries
// about.

// Step is one named unit of a CI reproduction.
//
// The name is what a failure reports. A bare exit code from the eleventh
// command in a sequence is a puzzle rather than a diagnosis.
type Step struct {
	Name string
	Run  func() error
}

// CIConfig declares how one repository reproduces its own pull-request
// workflow.
type CIConfig struct {
	// Steps run in order and fail fast. Order is part of the contract:
	// build precedes the race suite, and any binding generation precedes
	// the suites that load it.
	Steps []Step

	// Excluded names the CI jobs this reproduction does NOT run, one
	// short reason each. Printed on success. Empty means the local run
	// claims to be complete, which is a strong claim.
	Excluded []string
}

// RunCI executes the steps in order, stopping at the first failure.
func RunCI(cfg CIConfig) error {
	if len(cfg.Steps) == 0 {
		return fmt.Errorf("ci: no steps declared")
	}

	if dirty, err := treeIsDirty(); err == nil && dirty {
		fmt.Println("ci: WARNING - the working tree has uncommitted changes.")
		fmt.Println("ci: `nix build` and `nix flake check` see only GIT-TRACKED")
		fmt.Println("ci: files, so those steps test the committed state while")
		fmt.Println("ci: the mage steps test what is on disk. Commit before")
		fmt.Println("ci: trusting a green result from this run.")
	}

	total := len(cfg.Steps)
	for i, s := range cfg.Steps {
		fmt.Printf("\n=== ci [%d/%d] %s\n", i+1, total, s.Name)
		if err := s.Run(); err != nil {
			return fmt.Errorf("ci step %q failed: %w", s.Name, err)
		}
	}

	fmt.Printf("\nci: %d steps passed.\n", total)
	if len(cfg.Excluded) > 0 {
		fmt.Println("ci: NOT RUN HERE, and a green result above does not cover them:")
		for _, e := range cfg.Excluded {
			fmt.Println("ci:   - " + e)
		}
	}
	return nil
}

// Cmd builds a Step that runs one command with its output attached.
func Cmd(name, command string, args ...string) Step {
	return Step{Name: name, Run: func() error { return sh.RunV(command, args...) }}
}

// Target builds a Step from an existing mage target.
//
// Takes the function rather than invoking mage recursively so a failure
// surfaces as that target's own error rather than a subprocess exit code.
func Target(name string, fn func() error) Step {
	return Step{Name: name, Run: fn}
}

// NixBuild runs the Flake Build job's build step.
//
// The installable is spelled as CI spells it. `.#` RESOLVES ONLY
// GIT-TRACKED FILES, which is correct in CI, where the working tree is
// the commit, and is the reason RunCI warns on a dirty tree rather than
// silently substituting `path:.` - a local run that quietly tested
// something else would be worse than one that names the difference.
func NixBuild(installable string) error {
	return sh.RunV("nix", "build", installable, "--no-link", "--print-build-logs")
}

// NixFlakeCheckAllSystems runs the evaluation gate CI runs.
//
// `nix build` and `nix flake check` COVER DISJOINT FAILURE MODES and
// both are required. A required argument added to a package expression
// is an EVALUATION failure of every caller - a NixOS module among them -
// which no amount of building reaches. That defect merged once, past
// four green local builds.
func NixFlakeCheckAllSystems() error {
	return sh.RunV("nix", "flake", "check", "--no-build", "--all-systems")
}

// ProtoBaseline names a commit the working tree does not equal, so the
// schema's breaking check can actually fail.
//
// THIS IS THE SUBTLE ONE. A Proto target's own default is usually
// ".git#ref=HEAD", which is right for a developer comparing edits
// against the last commit and INERT anywhere the tree already IS that
// commit - the schema gets compared against itself and can never report
// a break. CI supersedes it with the pull request's merge base. A local
// `mage proto` alone therefore proves nothing about compatibility, and
// this function is what makes the local run mean the same thing.
//
// Resolution order: the merge base against origin/main, unless that is
// HEAD itself (already merged, or on main), in which case HEAD~1.
func ProtoBaseline() string {
	head, herr := gitRev("HEAD")
	base, berr := sh.Output("git", "merge-base", "origin/main", "HEAD")
	if berr == nil {
		base = strings.TrimSpace(base)
		if base != "" && (herr != nil || base != head) {
			return ".git#ref=" + base
		}
	}
	return ".git#ref=HEAD~1"
}

// WithProtoBaseline runs fn with PROTO_BREAKING_AGAINST set, restoring
// the previous value afterwards.
func WithProtoBaseline(baseline string, fn func() error) error {
	const key = "PROTO_BREAKING_AGAINST"
	prev, had := os.LookupEnv(key)
	if err := os.Setenv(key, baseline); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	defer func() {
		if had {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	}()
	return fn()
}

// AssertClean fails when any named path has uncommitted changes.
//
// This is the second half of CI's codegen gate: regenerating proves the
// generator runs, and only the diff proves the COMMITTED output is what
// the pinned plugins produce. Regeneration alone is a green that means
// nothing.
//
// Scoped to paths rather than CI's bare `git diff --exit-code`, and the
// difference is deliberate: CI runs on a fresh checkout where any diff
// is generated output, while a developer legitimately has unrelated edits
// in flight. Scoping keeps the gate honest without failing on work in
// progress.
func AssertClean(paths ...string) error {
	if len(paths) == 0 {
		return fmt.Errorf("ci: AssertClean needs at least one path")
	}
	args := append([]string{"diff", "--exit-code", "--"}, paths...)
	if err := sh.RunV("git", args...); err != nil {
		return fmt.Errorf(
			"generated output under %s differs from what the pinned "+
				"plugins produce; the committed code is stale: %w",
			strings.Join(paths, ", "), err)
	}
	return nil
}

func gitRev(ref string) (string, error) {
	out, err := sh.Output("git", "rev-parse", ref)
	return strings.TrimSpace(out), err
}

func treeIsDirty() (bool, error) {
	out, err := sh.Output("git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}
