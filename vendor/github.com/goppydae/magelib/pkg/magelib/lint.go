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

// SharedLintConfig resolves the checked-in golangci-lint configuration that
// every repo consumes: magelib's own copy when running inside magelib, the
// sibling checkout's copy otherwise. Carve-outs live there, rule-level and
// documented -- never per-line.
func SharedLintConfig() (string, error) {
	for _, candidate := range []string{".golangci.yml", "../magelib/.golangci.yml"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("shared .golangci.yml not found (looked in . and ../magelib); is the magelib sibling checked out?")
}

// Lint runs the full lint gate: gofmt check (fail on any unformatted file),
// then golangci-lint with the pinned shared config, then gosec.
//
// gosecExcludes is the rule-level carve-out mechanism: rule IDs passed here
// are excluded for the whole repo, and the call site in the repo's checked-in
// magefile is where the exclusion and its justification live. Per-line
// //nolint directives remain forbidden. Most repos pass nothing.
func Lint(gosecExcludes ...string) error {
	fmt.Println("Running lint gate (gofmt check, golangci-lint, gosec)...")

	files, err := goSourceFiles(".")
	if err != nil {
		return err
	}
	if len(files) > 0 {
		out, err := sh.Output("gofmt", append([]string{"-l"}, files...)...)
		if err != nil {
			return fmt.Errorf("gofmt check failed: %w", err)
		}
		if strings.TrimSpace(out) != "" {
			return fmt.Errorf("unformatted files (run 'mage fmt'):\n%s", out)
		}
	}

	cfg, err := SharedLintConfig()
	if err != nil {
		return err
	}
	if err := sh.RunV("golangci-lint", "run", "--config", cfg, "./..."); err != nil {
		return fmt.Errorf("golangci-lint: %w", err)
	}

	if err := sh.RunV("gosec", gosecArgs(gosecExcludes)...); err != nil {
		return fmt.Errorf("gosec: %w", err)
	}
	return nil
}

// gosecArgs builds the gosec command line for a set of rule-level
// carve-outs.
//
// Generated code (protoc-gen-go, cgo intermediates) is skipped: it is not
// hand-fixable, and rule-level excludes would also mute the same rule in
// hand-written code. The rule excludes stay the per-repo carve-out
// mechanism for code we own, and they go out as a single comma-joined
// -exclude flag because gosec takes the last one it is given.
func gosecArgs(excludes []string) []string {
	args := []string{"-exclude-generated"}
	if len(excludes) > 0 {
		args = append(args, "-exclude="+strings.Join(excludes, ","))
	}
	return append(args, "./...")
}

// Vuln runs govulncheck, the call-graph-filtered CVE gate. It is a named
// exception to hermeticity (it fetches the vulnerability DB over the
// network): when offline it is skipped loudly, never silently.
func Vuln() error {
	fmt.Println("Running govulncheck...")
	err := sh.RunV("govulncheck", "./...")
	if err == nil {
		return nil
	}
	if looksOffline(err) {
		fmt.Println("VULN: SKIPPED (offline) - govulncheck could not reach the vulnerability database.")
		fmt.Println("VULN: re-run 'mage vuln' when online; this skip is loud by design.")
		return nil
	}
	return fmt.Errorf("govulncheck: %w", err)
}

func looksOffline(err error) bool {
	msg := err.Error()
	for _, marker := range []string{"no such host", "connection refused", "network is unreachable", "i/o timeout", "TLS handshake timeout", "temporary failure in name resolution"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
