// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the second gate of GAPI-DIV-066: goblind must be able to
// name the gapi kernel it embeds. Before the fix it answered
// "Runtime Core: dev", and v0.1.0-proto2f shipped that way - in the very
// release that first embedded the new kernel.
//
// WHY THIS EXECS A BINARY INSTEAD OF CALLING THE CLI IN PROCESS, which
// is what every other test in this package does:
//
// The kernel version is resolved from debug.ReadBuildInfo, and a TEST
// binary carries NO dependency information at all. Measured at
// len(BuildInfo.Deps) == 0 from this very package, which links 38 gapi
// packages, while a shipped binary records
// "dep github.com/goppydae/gapi v0.1.0-proto2f". An in-process
// assertion on the kernel row would therefore read "dev" forever
// regardless of how correct the code is - it would be a gate that
// cannot run, reporting success.
//
// WHY IT SETS GOWORK=off ITSELF rather than inheriting the environment:
//
// Under go.work the toolchain resolves gapi from the ../gapi sibling and
// records "(devel)", which is honest and is not a version, so the kernel
// row correctly falls back. That would make this test pass in CI (which
// sets GOWORK=off) and fail on a developer's machine - and a test that
// is red locally by design acquires a t.Skip within a week. Owning the
// build mode makes it deterministic in both places, and vendored
// resolution is the mode that ships.

// buildGoblind compiles goblind under vendored resolution and returns
// the path to the binary.
func buildGoblind(t *testing.T) string {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locating repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "goblind")

	cmd := exec.Command("go", "build", "-mod=vendor", "-o", bin, "./cmd/goblind")
	cmd.Dir = repoRoot
	// GOWORK=off is the whole point; see the file comment.
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building goblind under vendored resolution: %v\n%s", err, out)
	}
	return bin
}

// vendoredGapiVersion reads the gapi version from vendor/modules.txt.
//
// This is deliberately a DIFFERENT reader from the one under test. The
// binary reports what the toolchain embedded in its build info; this
// reads what the vendor manifest declares. Asserting merely that the row
// is not "dev" would be satisfied by any hardcoded string, including a
// wrong one.
func vendoredGapiVersion(t *testing.T) string {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "vendor", "modules.txt"))
	if err != nil {
		t.Fatalf("opening vendor/modules.txt: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing vendor/modules.txt: %v", cerr)
		}
	}()

	const prefix = "# github.com/goppydae/gapi "
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading vendor/modules.txt: %v", err)
	}
	t.Fatal("vendor/modules.txt declares no gapi module; the kernel is not vendored")
	return ""
}

// versionRow pulls one labelled row out of the version block.
func versionRow(t *testing.T, block, label string) string {
	t.Helper()
	for _, line := range strings.Split(block, "\n") {
		name, value, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(name) == label {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("version block has no %q row:\n%s", label, block)
	return ""
}

// TestGoblindReportsTheKernelItEmbeds is GAPI-DIV-066's second gate.
//
// The existing guards in this package assert only that the string
// "Runtime Core" APPEARS - the label - so they pass identically whether
// the value is a real version or "dev". This asserts the value, against
// an independent declaration of it.
func TestGoblindReportsTheKernelItEmbeds(t *testing.T) {
	want := vendoredGapiVersion(t)
	// go.mod and modules.txt carry the "v" prefix; the version block does
	// not, matching the VERSION file's spelling.
	want = strings.TrimPrefix(want, "v")

	bin := buildGoblind(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("goblind version: %v\n%s", err, out)
	}
	block := string(out)

	got := versionRow(t, block, "Runtime Core")

	if got == "dev" {
		t.Fatalf("goblind cannot name the kernel it embeds - Runtime Core reads %q.\n"+
			"vendor/modules.txt declares gapi %q. GAPI-DIV-066.\n%s", got, want, block)
	}
	if got != want {
		t.Errorf("Runtime Core = %q, want %q from vendor/modules.txt\n%s", got, want, block)
	}
}

// TestGoblindReportsItsOwnVersionSeparately keeps the two version
// sources distinguishable. goblind stamps internal/version.Version and
// embeds gapi's core/version; a change that made the kernel row echo
// goblin's own version would satisfy the test above while still failing
// to answer the question it exists for.
func TestGoblindReportsItsOwnVersionSeparately(t *testing.T) {
	bin := buildGoblind(t)
	out, err := exec.Command(bin, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("goblind version: %v\n%s", err, out)
	}
	block := string(out)

	own := versionRow(t, block, "goblind")
	kernel := versionRow(t, block, "Runtime Core")

	if own == "" {
		t.Fatal("version block does not report goblind's own version")
	}
	// An unstamped `go build` leaves goblin's own version at "dev" while
	// the kernel row is resolved from build info, so these legitimately
	// differ here. The assertion is that the kernel row is not simply a
	// copy of goblin's - it must come from the module graph.
	if kernel == "dev" {
		t.Fatalf("kernel row is unresolved:\n%s", block)
	}
}
