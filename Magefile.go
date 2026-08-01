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
// Line count when the gate landed, for anyone judging progress:
// supervisor.go 1200 - 140 percent over the limit and the single largest
// hand-written violation in the silo. It is entangled with
// GOBLIN-DIV-038 (Supervisor.Run has no ordered shutdown, and the
// documented boot-phase model does not match the code), so the split is
// a cohesion question to settle with the startup/teardown ordering, not
// a line-count exercise. See GOBLIN-DIV-046.
var fileLengthWaivers = []string{
	"internal/supervisor/supervisor.go",
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
var fileLengthSkips = []string{}

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
// divergence.jsonl and deprecation.jsonl are skipped because
// GOBLIN-DIV-045 quotes both phrases, being about them, and would
// otherwise fail the gate it asked for.
func checkTerminology() error {
	return magelib.CheckTerminology(magelib.GoppydaeTerminologyRules,
		"divergence.jsonl", "deprecation.jsonl")
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
	mg.Deps(checkHermetic, checkTerminology, checkFileLength)
	return magelib.Lint("G402", "G404", "G304")
}

// Dev runs the development build and starts goblind
func Dev() error {
	mg.Deps(Build)
	fmt.Println("Starting goblind in development mode...")
	return sh.RunV("./bin/goblind")
}
