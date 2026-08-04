// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build mage
// +build mage

package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/goppydae/magelib/pkg/magelib"
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/zeebo/blake3"
)

// toolchain is this repo's single declaration of what the dev shell must
// provide. Doctor reports on it and checkHermetic gates on it, so the
// advisory check and the enforcing one read one value and cannot drift:
// MAGELIB-DIV-003 is exactly the drift that two declarations produce.
var toolchain = magelib.DoctorConfig{
	ReplaceTargets: []string{"../gapi", "../magelib"},
	ProtoPlugins:   []string{"buf", "protoc-gen-go", "protoc-gen-go-grpc"},
	GopyVersion:    "v0.4.10",
	RequiredEnv:    []string{"GOBIN"},
	SharedTools:    []string{"buf", "golangci-lint", "gosec", "govulncheck", "mage", "goimports", "mkdocs", "pandoc", "criu"},
}

// fileLengthWaivers is DEBT: hand-written files the 500-line rule applies
// to and that violate it today. The gate measures every one of them, and
// it FAILS once a file comes back at or under the limit, naming the
// waiver to delete - so this list can only ever shrink. Adding a line to
// this list is a decision to carry a violation, not to exempt a file.
//
// The list is EMPTY, and that is a result rather than a starting state:
// internal/supervisor/supervisor.go was the one entry, at 1200 lines
// against the 500 limit. It came off when GOBLIN-DIV-046 closed - the
// file was split along the boot phases while GOBLIN-DIV-038's ordered
// shutdown was built, so the boundaries follow the lifecycle rather
// than a line count. The gate forced the deletion: magelib fails a
// waiver whose file has come back under the limit, naming the waiver to
// remove, so the debt could not be paid off silently.
var fileLengthWaivers = []string{}

// fileLengthSkips is EXEMPTION, not debt: paths the 500-line rule does
// not reach at all. They are never measured, so they are never reported
// and never expected to shrink. A path may be a waiver or a skip, never
// both; the gate rejects claiming both as a config error.
//
// The list is EMPTY on purpose, not by oversight: this repo has no Go
// file the rule fails to reach. Everything generated here is protoc
// output under proto/, which carries the standard
// "// Code generated ... DO NOT EDIT." header that magelib's marker
// already recognises, so those files need no declaration; and nothing
// gitignored or vendored outside vendor/ lands a .go file in the walk.
// gapi needs an entry only because gopy stamps a non-standard header.
var fileLengthSkips = []magelib.Skip{}

// licenseNotice is goblin's declaration for the MPL header gate: a
// holder and a year, and deliberately nothing else. The Exhibit A prose
// and the SPDX line live in magelib and are never spelled here, which is
// the lesson the terminology gate paid for.
//
// Year is 2025, goblin's year of FIRST PUBLICATION, and it does not
// advance - a file added later still carries 2025, or the gate has to
// accept any year and stops discriminating. Per-repo years are gapi
// 2025, goblin 2025, magelib 2026, goppydae-docs 2026 (decision 16).
//
// There is deliberately no skip list to go with this, for the reason
// already given above fileLengthSkips: everything generated here is
// protoc output carrying the standard marker, which magelib's detector
// recognises by itself. gapi needs one only because of gopy.
var licenseNotice = magelib.LicenseConfig{
	Holder: "Steven Verhelle (enqack)",
	Year:   2025,
}

// versionLdflags stamps the resolved version into internal/version, the
// shared injection point read by both binaries (VERSION file is the source
// of version truth).
func versionLdflags() string {
	return "-X github.com/goppydae/goblin/internal/version.Version=" + magelib.Version()
}

// Build builds the goblind and goblinctl binaries
func Build() error {
	mg.Deps(checkHermetic)
	fmt.Printf("Building goblind and goblinctl (version %s)...\n", magelib.Version())

	// Goblin Daemon
	if err := sh.Run("go", "build", "-ldflags", versionLdflags(), "-o", "bin/goblind", "./cmd/goblind"); err != nil {
		return err
	}

	// Goblin CLI
	if err := sh.Run("go", "build", "-ldflags", versionLdflags(), "-o", "bin/goblinctl", "./cmd/goblinctl"); err != nil {
		return err
	}

	// Calculate and write BLAKE3 hashes
	for _, bin := range []string{"bin/goblind", "bin/goblinctl"} {
		if err := hashFile(bin); err != nil {
			// Don't fail hard if binary doesn't exist yet (e.g. partial build), but Build should succeed.
			return fmt.Errorf("hashing %s failed: %w", bin, err)
		}
	}

	fmt.Println("Build complete: bin/goblind, bin/goblinctl (with .b3 checksums)")
	return nil
}

func hashFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	sum := h.Sum(nil)
	hexSum := hex.EncodeToString(sum)

	return os.WriteFile(path+".b3", []byte(hexSum+"\n"), 0644)
}

// Install installs binaries to $GOPATH/bin
func Install() error {
	mg.Deps(checkHermetic)
	fmt.Println("Installing goblind and goblinctl...")

	if err := sh.Run("go", "install", "-ldflags", versionLdflags(), "./cmd/goblind"); err != nil {
		return err
	}

	if err := sh.Run("go", "install", "-ldflags", versionLdflags(), "./cmd/goblinctl"); err != nil {
		return err
	}

	fmt.Println("Installed to $GOPATH/bin")
	return nil
}

// Test runs all tests with the race detector (the suite CI runs)
func Test() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running tests (-race)...")
	return sh.RunV("go", "test", "-race", "./...")
}

// TestShort runs the fast inner-loop subset
func TestShort() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running short tests...")
	return sh.RunV("go", "test", "-short", "./...")
}

// Fuzz runs every Fuzz* target for a bounded fuzztime
func Fuzz() error {
	mg.Deps(checkHermetic)
	return magelib.FuzzAll("10s")
}

// Vuln runs govulncheck (named offline exception: skipped loudly when offline)
func Vuln() error {
	mg.Deps(checkHermetic)
	return magelib.Vuln()
}

// Doctor validates the local environment against the ecosystem pins
func Doctor() error {
	return magelib.Doctor(toolchain)
}

// CheckVersion gates the VERSION file against the tag being cut.
//
// Invoked by release-guard.yml on a tag ref, and by the operator before
// cutting one, which is the only form that PREVENTS rather than detects:
//
//	GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v0.1.0-proto2g mage checkVersion
//
// Deliberately not a dependency of Lint or Build. It errors off a tag
// ref by design, so wiring it into a target that runs on every pull
// request would make it permanently red or - the likelier repair -
// silence it into the no-op it must never become. See MAGELIB-DIV-006.
//
// goblin is the repo that proved the entry needed a gate rather than a
// procedure: v0.1.0-proto2f was tagged with VERSION stale and had to be
// deleted and re-cut, hours after the entry was filed and after the tag
// procedure had been written down to prevent exactly that.
//
// No mg.Deps(checkHermetic): this reads one file and two environment
// variables and depends on no ecosystem tool, so gating it on the
// hermetic check would let a dev-shell problem present itself as a
// version mismatch.
func CheckVersion() error {
	return magelib.CheckVersionAgainstTag()
}

// EnvCheck compares the sibling dev shells' tool inventories; skew is red
func EnvCheck() error {
	return magelib.CheckShellUnification(
		map[string]string{"gapi": "../gapi", "goblin": ".", "magelib": "path:../magelib"},
		[]string{"go", "gcc", "protoc", "buf", "golangci-lint", "gosec", "govulncheck", "mage", "goimports"},
	)
}

// TestCluster runs the process-based cluster suite (build tag 'cluster'
// keeps it out of the ordinary suite). The harness builds goblind and
// the fixture agent itself.
//
// This target carried '-run TestClusterEndToEnd' and so ran one of the
// two tests the tag compiles, leaving TestBootstrapExpect_* reachable
// from no target at all. There is no filter now: the tag decides what
// runs, and adding a cluster-tagged test is enough to get it gated.
//
// test/cluster/migration_test.go is tagged 'cluster && criu' and is not
// built here. TestMigration runs it.
func TestCluster() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running cluster suite (-race, -tags cluster)...")
	return sh.RunV("go", "test", "-race", "-tags", "cluster", "-timeout", "15m", "-v", "./test/cluster/")
}

// TestMigration runs the live-migration tests (build tags 'cluster' and
// 'criu'). They are separate from TestCluster because CRIU needs
// privileges a stock CI runner does not grant; the tests fail rather
// than skip when checkpointing is unavailable, by deliberate choice at
// migration_test.go:60, so running them where CRIU is absent is a red
// build and not a quiet pass.
func TestMigration() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running live-migration tests (-race, -tags cluster,criu)...")
	return sh.RunV("go", "test", "-race", "-tags", "cluster,criu", "-timeout", "15m", "-v", "./test/cluster/")
}

// TestPid1 runs the goblind PID-1 container smoke (Phase 0 before the
// cluster stack; reversed teardown as container init) via rootless podman.
func TestPid1() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running goblind PID-1 container smoke (rootless podman)...")
	return sh.RunV("go", "test", "-tags", "pid1", "-timeout", "10m", "-v", "-run", "TestGoblindPid1Smoke", "./test/pid1/")
}

// TestScheduler runs scheduler tests
func TestScheduler() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running scheduler tests (-race)...")
	return sh.RunV("go", "test", "-race", "./core/scheduler/...")
}

// Release cross-compiles pure-Go release artifacts (CGO_ENABLED=0) for
// linux/{amd64,arm64} and darwin/{amd64,arm64} into dist/, with a SHA256SUMS
// manifest. Signing (minisign) and the signed tag are operator-gated and
// printed as a stub.
func Release() error {
	mg.Deps(checkHermetic)
	return magelib.Release("goblin", "dist", versionLdflags(), map[string]string{
		"goblind":   "./cmd/goblind",
		"goblinctl": "./cmd/goblinctl",
	})
}

// Clean removes build artifacts
func Clean() error {
	fmt.Println("Cleaning build artifacts...")

	dirs := []string{"bin", "dist"}
	for _, dir := range dirs {
		if err := sh.Rm(dir); err != nil {
			fmt.Printf("Warning: failed to remove %s: %v\n", dir, err)
		}
	}

	fmt.Println("Clean complete")
	return nil
}

// Proto generates protobuf code through the buf gate (generate, lint,
// breaking). Generated code lands directly in proto/ (buf.gen.yaml
// module opt strips the module prefix from go_package).
func Proto() error {
	mg.Deps(checkHermetic)
	// ".git#ref=HEAD" compares an edited working tree against the last
	// commit, which is right for a developer and inert in CI, where the
	// working tree IS the commit and the schema would be compared
	// against itself. CI names its own baseline - the pull request's
	// merge base - through PROTO_BREAKING_AGAINST, which magelib reads
	// and which supersedes this value.
	if err := magelib.BufGenerate(".git#ref=HEAD"); err != nil {
		return err
	}
	fmt.Println("Protobuf generation complete (buf generate + lint + breaking)")
	return nil
}

// Fmt formats all Go code
func Fmt() error {
	return magelib.Fmt()
}

// Tidy runs go mod tidy
func Tidy() error {
	return magelib.Tidy()
}

// checkHermetic ensures tools are running from Nix store
func checkHermetic() error {
	return magelib.CheckHermetic(toolchain.SharedTools...)
}

// checkTerminology enforces the silo's naming rules through magelib.
// The rules themselves live there, not here: a Magefile sits at the
// repo root and is walked, so declaring the phrases inline would trip
// the gate on its own declaration.
//
// The two ledgers are skipped; each skip states its own reason at the
// call below, which is where a reviewer can audit the claim.
func checkTerminology() error {
	return magelib.CheckTerminology(magelib.GoppydaeTerminologyRules,
		magelib.Skip{
			Name: "divergence.jsonl",
			Reason: "the divergence ledger quotes what it polices: " +
				"GOBLIN-DIV-045 is the entry that corrected GAPI's expansion " +
				"across this repo, and it cannot record which expansion it " +
				"rejected without writing that expansion down. Measured " +
				"2026-08-01: exactly one line matches, the violation field " +
				"of that entry, on the expansion rule - so the gate would go " +
				"red against the entry that asked for it. The styling rule " +
				"matches nothing here; its occurrences in that entry sit " +
				"inside Go identifiers, which WordBoundary spares by design.",
		},
		magelib.Skip{
			Name: "deprecation.jsonl",
			Reason: "the deprecation ledger is the sibling surface of the " +
				"same exemption: retiring a name means writing the retired " +
				"name down, so an entry that deprecates a term must quote " +
				"it. This file is zero bytes - goblin has deprecated nothing " +
				"yet - so the skip guards no violation today and removing it " +
				"changes no result (measured 2026-08-01). It is granted for " +
				"the record the file exists to hold, and is inert until that " +
				"record has a first entry.",
		},
	)
}

// checkFileLength enforces the manifesto's 500-line limit on hand-written
// Go. The two lists it passes make opposite promises - see their
// declarations above - and keeping them apart is what lets the waiver
// list function as a burndown rather than a permanent carve-out.
func checkFileLength() error {
	return magelib.CheckFileLength(fileLengthWaivers, fileLengthSkips...)
}

// All runs fmt, tidy, build, and test
func All() error {
	mg.Deps(Fmt, Tidy, Build, Test)
	fmt.Println("All tasks complete")
	return nil
}

// Documentation tasks
type Docs mg.Namespace

// Html generates the static documentation site using MkDocs
func (Docs) Html() error {
	fmt.Println("Generating HTML documentation...")
	if _, err := exec.LookPath("mkdocs"); err != nil {
		return fmt.Errorf("mkdocs not found. Run 'nix develop' to get documentation tools")
	}
	return sh.RunV("mkdocs", "build")
}

// Man generates man pages from markdown files using Pandoc
func (Docs) Man() error {
	fmt.Println("Generating Man pages...")
	// Check for pandoc
	if _, err := exec.LookPath("pandoc"); err != nil {
		return fmt.Errorf("pandoc not found. Run 'nix develop' to get documentation tools")
	}

	if err := os.MkdirAll("man/man1", 0755); err != nil {
		return err
	}

	// Generate man pages for Goblin commands
	pages := map[string]string{
		"docs/index.md":           "man/man1/goblin.1",
		"docs/getting-started.md": "man/man1/goblin-quickstart.1",
	}

	for src, dst := range pages {
		// Check if source exists
		if _, err := os.Stat(src); os.IsNotExist(err) {
			fmt.Printf("Skipping %s (not found)\n", src)
			continue
		}

		fmt.Printf("Generating %s -> %s\n", src, dst)
		if err := sh.Run("pandoc", src, "-s", "-t", "man", "-o", dst); err != nil {
			return fmt.Errorf("failed to generate %s: %w", dst, err)
		}
	}

	fmt.Println("Man pages generated in ./man/man1")
	return nil
}

// TestUnit runs only unit tests (core and internal packages)
func TestUnit() error {
	mg.Deps(checkHermetic)
	fmt.Println("Running unit tests...")
	return sh.RunV("go", "test", "-v", "./core/...", "./internal/...")
}

// Lint runs the shared lint gate (gofmt check, pinned golangci-lint, gosec).
//
// Rule-level gosec carve-outs (GOBLIN-DIV-034):
//   - G402: the InsecureSkipVerify fallbacks are dev-mode only; production
//     mode fails closed at startup when TLS is not configured (supervisor
//     Run, covered by TestRun_ProductionModeRequiresTLS).
//   - G404: math/rand is scheduler placement jitter; there is no adversary
//     and crypto/rand would add error paths to hot paths for no benefit.
//   - G304: fires only in test/cluster/gen_certs.go, a dev-cert fixture
//     generator whose path segments are validated (nodeIDPattern) and
//     joined under a constant certDir.
func Lint() error {
	mg.Deps(checkHermetic, checkTerminology, checkFileLength, LicenseCheck)
	return magelib.Lint("G402", "G404", "G304")
}

// LicenseCheck reports every hand-written file missing the MPL notice.
//
// A dependency of Lint as of the sweep that headered this repo. Wiring
// it BEFORE the sweep would have turned every CI run red, so sweep and
// gate land together and the gate is never a mechanism with no caller.
func LicenseCheck() error {
	return magelib.CheckLicenseHeaders(licenseNotice)
}

// LicenseAdd inserts the notice into every file LicenseCheck reports.
//
// AddLicenseHeaders prints each modified path and the total itself, so
// the returned slice is discarded rather than printed twice. That list
// is what the `git add` line is built from.
func LicenseAdd() error {
	_, err := magelib.AddLicenseHeaders(licenseNotice)
	return err
}

// Dev runs the development build and starts goblind
func Dev() error {
	mg.Deps(Build)
	fmt.Println("Starting goblind in development mode...")
	return sh.RunV("./bin/goblind", "start")
}
