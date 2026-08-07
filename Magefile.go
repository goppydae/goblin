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
	// GopyVersion is deliberately UNSET, so doctor reports
	// "no FFI codegen tool required" rather than checking for gopy.
	//
	// goblin generates no Python bindings. Operator decision 49 has it
	// take gapi as a flake input and copy the ADK gapi has already
	// BUILT; nothing here runs gopy, and no target in this repo can.
	// The check was asserting a capability this repo does not use, and
	// the shell hook built a gopy into $GOBIN to satisfy it - which is
	// how goblin's shell came to write a stale binary into a SIBLING's
	// .bin and break gapi's hermetic gate (GOBLIN-DIV-077).
	RequiredEnv: []string{"GOBIN"},
	// hugo replaced mkdocs and pandoc when the generated site landed. All
	// three are documentation tools and that is the whole resemblance:
	// mkdocs rendered a hand-written tree, and pandoc converted pages one
	// of which did not exist and was skipped in silence. Both targets are
	// retired, so leaving their tools declared would keep two entries the
	// doctor requires, the hermetic check gates on, and no target uses.
	SharedTools: []string{"buf", "golangci-lint", "gosec", "govulncheck", "mage", "goimports", "hugo", "criu"},
}

// fileLengthWaivers is DEBT: hand-written files the 500-line rule applies
// to and that violate it today. The gate measures every one of them, and
// it FAILS once a file comes back at or under the limit, naming the
// waiver to delete - so this list can only ever shrink. Adding a line to
// this list is a decision to carry a violation, not to exempt a file.
//
// The list WAS empty, and that was a result rather than a starting
// state: internal/supervisor/supervisor.go was the one entry, at 1200
// lines against the 500 limit. It came off when GOBLIN-DIV-046 closed -
// the file was split along the boot phases while GOBLIN-DIV-038's
// ordered shutdown was built, so the boundaries follow the lifecycle
// rather than a line count. The gate forced the deletion: magelib fails
// a waiver whose file has come back under the limit, naming the waiver
// to remove, so the debt could not be paid off silently.
//
// IT STOPPED BEING EMPTY WHEN `CI` LANDED, and that is a decision rather
// than an accident. This file sat at 490 lines - ten under the limit -
// which made it a trap for the next change rather than for the one that
// put it there. `CI` and `CIVM` are 52 lines, so it went over.
//
// Waived rather than split, following operator decision 23: magefiles
// stay monolithic. A second magefile in this directory WOULD have worked
// and needed no waiver at all - mage reads every such file - and that is
// exactly the trade gapi's own waiver records rejecting, on the grounds
// that one discoverable Magefile is worth more than a clean list.
//
// THE EXIT IS TO SHORTEN THIS FILE, NOT TO GROW THIS LIST.
var fileLengthWaivers = []string{
	"Magefile.go",
}

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

// checkLedger gates the divergence ledger's STRUCTURE, not its content.
//
// The ledger is this project's honesty layer and nothing checked its
// shape: `jq -e .` proves each line is JSON and says nothing about which
// keys it carries. Two entries were filed without an `opened` date and
// nothing noticed until one was rendered by hand to read it.
func checkLedger() error {
	if err := magelib.CheckDivergence("divergence.jsonl"); err != nil {
		return err
	}
	return magelib.CheckDeprecation("deprecation.jsonl")
}

// All runs fmt, tidy, build, and test
func All() error {
	mg.Deps(Fmt, Tidy, Build, Test)
	fmt.Println("All tasks complete")
	return nil
}

// CI reproduces ci.yml's pull-request jobs locally, in CI's order.
//
// NOT `All`. All runs Fmt and Tidy, which REPAIR the tree, so it cannot
// fail the way CI fails: an unformatted file is fixed by All and
// REJECTED by lint. An audit against the workflows found All covering
// two of the fourteen things CI runs.
func CI() error {
	return magelib.RunCI(magelib.CIConfig{
		Steps: []magelib.Step{
			// Environment first. A wrong toolchain makes every result
			// below meaningless rather than wrong.
			magelib.Target("doctor", Doctor),
			magelib.Target("envcheck", EnvCheck),
			magelib.Target("lint", Lint),

			magelib.Target("build", Build),
			magelib.Target("vuln", Vuln),
			magelib.Target("test", Test),
			magelib.Target("testCluster", TestCluster),

			// Regenerating proves the generator runs; only the diff
			// proves the COMMITTED output is what the pinned plugins
			// produce. The baseline matters as much: Proto's own default
			// compares the tree against itself and can never fail.
			magelib.Step{Name: "proto (breaking against the merge base)", Run: func() error {
				if err := magelib.WithProtoBaseline(magelib.ProtoBaseline(), Proto); err != nil {
					return err
				}
				return magelib.AssertClean("pkg/proto")
			}},

			magelib.Step{Name: "nix build", Run: func() error { return magelib.NixBuild(".#") }},
			magelib.Step{Name: "nix flake check --all-systems", Run: magelib.NixFlakeCheckAllSystems},
		},
		Excluded: []string{
			"No-Replace Module Resolution - runs `rm -rf vendor` and resolves " +
				"from the module proxy under a read token; this repo COMMITS vendor/",
			"VM Checks - deliberately off the pull-request path (vm-checks.yml); " +
				"run `mage ciVM`",
			"Integration Against Kernel Main - CI checks out gapi's MAIN and builds " +
				"against it; go.work here resolves ../gapi at whatever it is checked " +
				"out to, which is more useful for development and is NOT the same assertion",
			"release-guard checkVersion - fires on a tag push, not a pull request",
		},
	})
}

// CIVM runs vm-checks.yml: the guest-booting gate, which needs KVM and
// minutes and is deliberately NOT on the pull-request path.
func CIVM() error {
	return sh.RunV("nix", "flake", "check", "--print-build-logs",
		"--max-jobs", "1", "--keep-going")
}

// docsConfig is this repo's documentation site.
//
// Generators is what gives tools/gendocs a caller. GOBLIN-DIV-078: the
// content tree existed with no Hugo configuration, no docs workflow on
// any branch, and zero occurrences of magelib.DocsConfig here, so
// DocsSync, DocsBuild and the drift gate were all unreachable.
//
// The generator is invoked with ONE APPENDED ARGUMENT, the output root.
// That is magelib's contract and it is what makes the drift gate
// trustworthy: CheckDocsDrift regenerates into a temporary tree and
// byte-compares, which it can only do if generation is redirectable. A
// generator that hardcoded its output would force the gate to either
// mutate the working tree - repairing the drift it is measuring - or
// compare against something it did not produce.
//
// APIPackages is empty, so there is no gomarkdoc target: goblin
// publishes an operator reference, and its exported Go surface is
// served by pkg.go.dev, which the sidebar links.
//
// There is no configuration reference either, and that is a real
// difference from gapi rather than an omission - goblin declares no
// config schema, so there is nothing to join defaults against.
var docsConfig = magelib.DocsConfig{
	Dir:        "docs",
	Title:      "goblin",
	BaseURL:    "https://goppydae.github.io/goblin/",
	Repo:       "github.com/goppydae/goblin",
	Generators: [][]string{{"go", "run", "./tools/gendocs"}},
	Committed:  docsCommitted,
}

// docsCommitted are the generated paths under drift control.
//
// Produced by tools/gendocs and checked in, so the reference reads on
// the forge without a build. Exhaustive by necessity rather than by
// style: CheckDocsDrift reports a generated file this list omits as
// UNTRACKED and fails, which is what stops a new command's page from
// escaping the gate. Listing a directory would give up that check.
var docsCommitted = []string{
	// The goblinctl and goblind command trees. goblinctl mounts the
	// kernel's agent verbs under `agent`, so its pages cover both
	// goblin's own surface and the embedded supervisor's.
	"docs/content/reference/cli/goblinctl/goblinctl.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_build.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_clean.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_age-keygen.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_decrypt.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_encrypt.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_keygen.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_sign.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_crypto_verify.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle_reload.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle_restart.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle_start.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle_status.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_lifecycle_stop.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_new.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_ping.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_reload.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_shutdown.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_status.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_tui.md",
	"docs/content/reference/cli/goblinctl/goblinctl_agent_verify.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_delete.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_get.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_instances.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_list.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_register.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_scale.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_agent_signal.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_drain.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_migrate-instance.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_migrate.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_publish.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_run.md",
	"docs/content/reference/cli/goblinctl/goblinctl_cluster_status.md",
	"docs/content/reference/cli/goblinctl/goblinctl_operator.md",
	"docs/content/reference/cli/goblinctl/goblinctl_operator_keygen.md",
	"docs/content/reference/cli/goblinctl/goblinctl_tui.md",
	"docs/content/reference/cli/goblinctl/goblinctl_version.md",
	"docs/content/reference/cli/goblind/goblind.md",
	"docs/content/reference/cli/goblind/goblind_start.md",
	"docs/content/reference/cli/goblind/goblind_version.md",

	// Man pages, section 1, from the same two trees.
	"docs/man/man1/goblinctl-agent-build.1",
	"docs/man/man1/goblinctl-agent-clean.1",
	"docs/man/man1/goblinctl-agent-crypto-age-keygen.1",
	"docs/man/man1/goblinctl-agent-crypto-decrypt.1",
	"docs/man/man1/goblinctl-agent-crypto-encrypt.1",
	"docs/man/man1/goblinctl-agent-crypto-keygen.1",
	"docs/man/man1/goblinctl-agent-crypto-sign.1",
	"docs/man/man1/goblinctl-agent-crypto-verify.1",
	"docs/man/man1/goblinctl-agent-crypto.1",
	"docs/man/man1/goblinctl-agent-lifecycle-reload.1",
	"docs/man/man1/goblinctl-agent-lifecycle-restart.1",
	"docs/man/man1/goblinctl-agent-lifecycle-start.1",
	"docs/man/man1/goblinctl-agent-lifecycle-status.1",
	"docs/man/man1/goblinctl-agent-lifecycle-stop.1",
	"docs/man/man1/goblinctl-agent-lifecycle.1",
	"docs/man/man1/goblinctl-agent-new.1",
	"docs/man/man1/goblinctl-agent-ping.1",
	"docs/man/man1/goblinctl-agent-reload.1",
	"docs/man/man1/goblinctl-agent-shutdown.1",
	"docs/man/man1/goblinctl-agent-status.1",
	"docs/man/man1/goblinctl-agent-tui.1",
	"docs/man/man1/goblinctl-agent-verify.1",
	"docs/man/man1/goblinctl-agent.1",
	"docs/man/man1/goblinctl-cluster-agent-delete.1",
	"docs/man/man1/goblinctl-cluster-agent-get.1",
	"docs/man/man1/goblinctl-cluster-agent-instances.1",
	"docs/man/man1/goblinctl-cluster-agent-list.1",
	"docs/man/man1/goblinctl-cluster-agent-register.1",
	"docs/man/man1/goblinctl-cluster-agent-scale.1",
	"docs/man/man1/goblinctl-cluster-agent-signal.1",
	"docs/man/man1/goblinctl-cluster-agent.1",
	"docs/man/man1/goblinctl-cluster-drain.1",
	"docs/man/man1/goblinctl-cluster-migrate-instance.1",
	"docs/man/man1/goblinctl-cluster-migrate.1",
	"docs/man/man1/goblinctl-cluster-publish.1",
	"docs/man/man1/goblinctl-cluster-run.1",
	"docs/man/man1/goblinctl-cluster-status.1",
	"docs/man/man1/goblinctl-cluster.1",
	"docs/man/man1/goblinctl-operator-keygen.1",
	"docs/man/man1/goblinctl-operator.1",
	"docs/man/man1/goblinctl-tui.1",
	"docs/man/man1/goblinctl-version.1",
	"docs/man/man1/goblinctl.1",
	"docs/man/man1/goblind-start.1",
	"docs/man/man1/goblind-version.1",
	"docs/man/man1/goblind.1",

	// Section 7, converted from the written overview rather than
	// generated from a command tree.
	"docs/man/man7/goblin.7",
}

// Documentation tasks
type Docs mg.Namespace

// Sync materialises the shared Hugo assets into docs/.magelib.
func (Docs) Sync() error {
	mg.Deps(checkHermetic)
	return magelib.DocsSync(docsConfig)
}

// Generate renders the reference from source.
func (Docs) Generate() error {
	mg.Deps(checkHermetic)
	return magelib.DocsGenerate(docsConfig)
}

// Build renders the static site into docs/public.
func (Docs) Build() error {
	mg.Deps(checkHermetic)
	return magelib.DocsBuild(docsConfig)
}

// Serve runs Hugo's own server with live reload.
func (Docs) Serve() error {
	mg.Deps(checkHermetic)
	return magelib.DocsServe(docsConfig)
}

// Check fails when the committed reference no longer matches its source.
//
// Wired into Lint rather than left to a separate invocation, because a
// gate nobody runs is the state this reference was already in: 312
// hand-written lines of cli-reference.md that nothing compared to the
// command tree.
func (Docs) Check() error {
	mg.Deps(checkHermetic)
	return magelib.CheckDocsDrift(docsConfig)
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
//   - G304: THE STATED REASON FOR THIS ONE WAS FALSE. It read "fires only
//     in test/cluster/gen_certs.go, a dev-cert fixture generator"; that
//     fixture is now deleted and G304 still fires at three PRODUCTION
//     sites - internal/cli/operator.go:64 (writing the operator private
//     key to an operator-supplied path), core/migration/client.go:121
//     (writing a checkpoint received from a peer) and
//     core/migration/server.go:161 (reading one back, which already
//     carries its own inline //nolint:gosec with a justification). So the
//     rule-level exclusion covers the key writer and the checkpoint
//     writer, both of which handle paths the process did not choose,
//     which is exactly what G304 is for. Kept rather than dropped because
//     removing it turns lint red and that is a security decision, not a
//     cleanup. Filed as GOBLIN-DIV-076.
func Lint() error {
	mg.Deps(checkHermetic, checkTerminology, checkFileLength, checkLedger, LicenseCheck, Docs.Check)
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
