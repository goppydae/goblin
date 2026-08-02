package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	gapiproduct "github.com/goppydae/gapi/core/product"
)

// This is GOBLIN-DIV-055's gate.
//
// The entry's exit asked for a scan that fails on any surviving RUNTIME_
// name and on any goblin-owned name missing the GOBLIN_ prefix. That is
// necessary and it is not sufficient, because it measures goblin's tree
// against a CONSTANT while the defect is a DISAGREEMENT: goblin exporting
// a variable the embedded kernel does not read. Both halves can be
// individually well-formed and still not meet.
//
// So the Go call sites do not spell these names at all - they compose
// them through the vendored kernel's own registry (see agentPathEnv in
// test/cluster/harness_test.go), which makes drift impossible rather than
// detectable. What remains is the sites that CANNOT compose: nix
// expressions and shell. Those are what this file checks, by rendering
// the composed name and asserting the literal matches it.
//
// The nix half matters more than its size suggests.
// nix/tests/cluster-migration.nix is executed only by VM Checks, which is
// off goblin's pull-request path, so without this gate that fence site is
// verified by nothing until after a merge.

// productName is goblin's product identity, the value both binary roots
// declare. Set here rather than imported so this test fails if the value
// the roots pass ever diverges from the value the fixtures assume.
const productName = "goblin"

// composedNames are the kernel-owned settings goblin or its fixtures pass
// to a spawned goblind, mapped to the registry suffix that composes them.
var composedNames = map[string]string{
	"AGENT_PATH":           "names a fixture directory, PREPENDED to the search path",
	"AGENT_PATH_EXCLUSIVE": "fences discovery to what AGENT_PATH names",
	"VERIFY_KEY":           "agent signing public key",
	"PY_RUNNER":            "Python ADK runner path",
	"KMSG_PATH":            "kmsg device override under --pid1",
}

// oldPrefixRe is the namespace the kernel owned before GAPI-DIV-059.
var oldPrefixRe = regexp.MustCompile(`\b(?:RUNTIME|GAPID)_[A-Z0-9_]+\b`)

// gapiPrefixRe is the kernel's CURRENT namespace. A GAPI_ name in
// goblin's CODE is the GAPI-DIV-061 defect in its original form: goblind
// reading a variable named for a library its operators never installed.
//
// Applied to code only, and the exclusion of documentation is a real
// distinction rather than a convenience. RUNTIME_ is dead in BOTH
// products, so it must not survive in any file. GAPI_ is alive and
// correct for gapid - docs/ecosystem.md explains gapid's own
// configuration, and forbidding the kernel's namespace there would make
// the document unable to describe the thing it exists to describe.
var gapiPrefixRe = regexp.MustCompile(`\bGAPI_[A-Z0-9_]+\b`)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func scanned(rel string) bool {
	if strings.HasPrefix(rel, "vendor/") || strings.HasPrefix(rel, ".git/") {
		return false
	}
	switch filepath.Ext(rel) {
	case ".go", ".nix", ".sh", ".md", ".yml", ".yaml":
		return true
	}
	return false
}

func walkTree(t *testing.T, visit func(rel, body string)) {
	t.Helper()
	root := repoRoot(t)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || !scanned(rel) {
			return nil //nolint:nilerr // an unrelatable path is not ours to scan
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		visit(rel, string(raw))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// Every literal environment name goblin passes to a goblind agrees with
// the name the embedded kernel composes for that setting.
//
// This is the assertion the entry's literal scan could not make. It reads
// the same registry the kernel reads, so if GAPI-DIV-061's composition
// ever changes shape, the nix fixtures go red here instead of going
// quiet in a VM nobody runs before merging.
func TestEnvNames_LiteralsAgreeWithTheKernel(t *testing.T) {
	gapiproduct.Set(productName)

	// A literal is acceptable only if it is one of the composed names.
	acceptable := map[string]string{}
	for suffix, why := range composedNames {
		acceptable[gapiproduct.EnvKey(suffix)] = why
	}

	// Anything shaped like one of our settings but spelled differently.
	// AGENT_PATH_EXCLUSIVE is listed before AGENT_PATH for readability
	// only. The ordering is NOT load-bearing, which was worth confirming
	// rather than assuming: a shorter alternative that matches and then
	// fails the trailing \b does not mask the longer one, because Go's
	// RE2 engine keeps every alternative alive rather than committing to
	// the first. Checked by swapping the two with a deliberately wrong
	// EXCLUSIVE literal in the tree - still red, both orderings.
	suspect := regexp.MustCompile(`\b[A-Z][A-Z0-9]*_(?:AGENT_PATH_EXCLUSIVE|AGENT_PATH|VERIFY_KEY|PY_RUNNER|KMSG_PATH)\b`)

	var wrong []string
	walkTree(t, func(rel, body string) {
		if rel == "internal/cli/envnames_test.go" {
			return // the declarations above are not uses
		}
		for _, m := range suspect.FindAllString(body, -1) {
			if _, ok := acceptable[m]; ok {
				continue
			}
			wrong = append(wrong, rel+": "+m)
		}
	})

	if len(wrong) > 0 {
		sort.Strings(wrong)
		var want []string
		for name := range acceptable {
			want = append(want, name)
		}
		sort.Strings(want)
		t.Errorf("environment names that the embedded kernel does not read.\n"+
			"The kernel composes these from goblin's product identity, so a "+
			"literal spelled any other way is set by goblin and read by nobody - "+
			"and the failure is SILENT: discovery stops being fenced, falls back "+
			"to the default search path, and the cluster tests fail as a "+
			"placement timeout indistinguishable from a flake (GOBLIN-DIV-055).\n"+
			"found:\n  %s\nthe kernel reads:\n  %s",
			strings.Join(wrong, "\n  "), strings.Join(want, "\n  "))
	}
}

// No pre-rename name survives, and no kernel-namespaced name appears.
func TestEnvNames_NoForeignNamespaceSurvives(t *testing.T) {
	var found []string
	walkTree(t, func(rel, body string) {
		if rel == "internal/cli/envnames_test.go" {
			return
		}
		for _, m := range oldPrefixRe.FindAllString(body, -1) {
			found = append(found, rel+": "+m+" (pre-GAPI-DIV-059 namespace)")
		}
		if filepath.Ext(rel) == ".md" {
			return // see gapiPrefixRe: documentation may name gapid's namespace
		}
		for _, m := range gapiPrefixRe.FindAllString(body, -1) {
			found = append(found, rel+": "+m+" (the kernel's namespace, not goblin's)")
		}
	})
	if len(found) > 0 {
		sort.Strings(found)
		t.Errorf("environment names outside goblin's namespace survive.\n"+
			"goblind's operators have never heard of the kernel, so every name "+
			"they set carries GOBLIN_ (GOBLIN-DIV-055):\n  %s",
			strings.Join(dedupe(found), "\n  "))
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
