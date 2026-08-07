// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CheckShellUnification compares the tool inventory of sibling dev shells by
// resolving each tool's real store path inside every shell. Identical shells
// resolve identical paths; any skew is returned as an error, turning silent
// drift into a red check (ecosystem manifesto section 11, interim measure --
// the shared-nix-module refactor is the recorded target).
//
// shells maps a display name to a flake ref (e.g. "gapi" -> "../gapi").
// The check shells out to `nix develop <ref>`, so it is slower than the
// doctor and lives behind its own target.
// missingDeclaration is what a probe reports when a shell resolves no
// tool, or a go.mod declares no require, for a name that was asked
// about. It is a sentinel rather than a value, so two of them are not
// evidence of agreement.
const missingDeclaration = "MISSING"

// inventory pairs a source's display name with the values it declared,
// keyed by whatever was probed for - a tool name for a dev shell, a
// module path for a go.mod.
type inventory struct {
	name   string
	values map[string]string
}

// compareInventories reports keys whose declared values differ between
// sources (skew) and keys some source did not declare at all
// (unresolved). The two are separate because they want opposite
// responses: skew means converge the declarations, unresolved means the
// thing is not declared in that source at all.
//
// Before the split, an unresolved tool compared equal to another
// unresolved tool, so a tool absent from EVERY shell read as unified and
// the gate went green.
//
// absentPhrase names what absence means for the source type being
// compared ("not resolvable", "not declared"), because the remedy
// differs and a message that says "resolvable" about a require directive
// sends the reader to the wrong file.
//
// It is pure so it can be tested; the probes that feed it are not.
func compareInventories(inv []inventory, keys []string, absentPhrase string) (skew, unresolved []string) {
	for _, key := range keys {
		var absent []string
		for _, src := range inv {
			if src.values[key] == missingDeclaration || src.values[key] == "" {
				absent = append(absent, src.name)
			}
		}
		if len(absent) > 0 {
			unresolved = append(unresolved, fmt.Sprintf(
				"%s: %s in %s", key, absentPhrase, strings.Join(absent, ", ")))
			continue
		}
		ref := inv[0]
		for _, other := range inv[1:] {
			a, b := ref.values[key], other.values[key]
			if a != b {
				skew = append(skew, fmt.Sprintf("%s: %s=%s vs %s=%s",
					key, ref.name, a, other.name, b))
			}
		}
	}
	return skew, unresolved
}

// sortedNames returns a map's keys in a stable order.
//
// Map iteration order decides which source becomes the reference in
// compareInventories, so without this the SAME skew is reported as
// "gapi=X vs goblin=Y" on one run and "goblin=Y vs gapi=X" on the next.
// Both are true and neither is reproducible, which is the shape a
// flaky-looking gate has when nothing is actually flaky.
func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ModulePins declares a versioned pin that lives in a FILE rather than in
// a dev shell, and that must agree across a set of repositories.
//
// The shell half of this gate answers "do these repos resolve the same
// tool?". It cannot answer "do these repos pin the same Hugo theme?",
// because that pin is not a tool in a devshell at all - it is a require
// directive in a nested docs/go.mod, one per repo, with nothing comparing
// them. Same defect class, different file type, so it reuses the same
// comparator rather than growing a second one with its own bugs.
//
// Dirs is a separate map from CheckShellUnification's shells rather than
// being derived from it. That map holds FLAKE REFS ("path:../magelib",
// "."), and turning a flake ref back into a directory is string surgery
// that would be wrong for exactly the spellings nobody tests. Naming the
// directory costs one line per repo and cannot be wrong quietly.
type ModulePins struct {
	// Dirs maps a display name to a repository directory.
	Dirs map[string]string
	// File is the repo-relative go.mod to read, e.g. "docs/go.mod".
	File string
	// Modules are the module paths whose versions must agree.
	//
	// A module absent from one repo's file is REPORTED, not skipped.
	// An absent pin and an agreeing pin are the same silence otherwise,
	// which is the bug compareInventories' skew/unresolved split exists
	// to prevent.
	Modules []string
}

// validate rejects a pin set that cannot mean what the caller intended.
//
// Every rejection is an error rather than a warning, for the same reason
// compileSkips rejects a whole-tree skip: a gate must not run at all on
// a configuration that would make it green without checking anything.
func (p ModulePins) validate() error {
	if strings.TrimSpace(p.File) == "" {
		return fmt.Errorf("module pin check: File is empty; name the go.mod to read, e.g. \"docs/go.mod\"")
	}
	if filepath.IsAbs(p.File) {
		return fmt.Errorf("module pin check: File %q must be repo-relative, not absolute", p.File)
	}
	if len(p.Modules) == 0 {
		return fmt.Errorf("module pin check: %s names no modules; a pin set that compares nothing reports agreement it never checked", p.File)
	}
	if len(p.Dirs) < 2 {
		return fmt.Errorf("module pin check: %s names %d repository, need at least two to compare", p.File, len(p.Dirs))
	}
	return nil
}

// goModJSON is the subset of `go mod edit -json` output this gate reads.
type goModJSON struct {
	Require []struct {
		Path    string
		Version string
	}
}

// pinInventories reads each repository's go.mod through `go mod edit
// -json` and records the version it requires for every named module.
//
// The parse is delegated to the go command rather than hand-rolled.
// A require directive can be a block or a line, carry an // indirect
// comment, and sit beside replace and exclude directives; a scanner that
// gets any of that wrong reports a version skew that does not exist, or
// worse, misses one. `go` is already in every shell this gate compares,
// so the canonical parser costs no new dependency in a library that
// deliberately has one.
func pinInventories(p ModulePins) ([]inventory, error) {
	var inv []inventory
	for _, name := range sortedNames(p.Dirs) {
		path := filepath.Join(p.Dirs[name], p.File)
		out, err := exec.Command("go", "mod", "edit", "-json", path).Output()
		if err != nil {
			return nil, fmt.Errorf("reading %s (%s): %w", path, name, err)
		}
		var parsed goModJSON
		if err := json.Unmarshal(out, &parsed); err != nil {
			return nil, fmt.Errorf("parsing %s (%s): %w", path, name, err)
		}
		values := map[string]string{}
		for _, r := range parsed.Require {
			values[r.Path] = r.Version
		}
		inv = append(inv, inventory{name: name, values: values})
	}
	return inv, nil
}

// shellInventories resolves every tool's real store path inside every
// dev shell.
// gobinEntries lists the file names in the caller's GOBIN.
//
// The shared idiom across these repos is GOBIN=$PWD/.bin with GOBIN
// leading PATH, and $PWD belongs to WHOEVER ENTERED THE SHELL, because
// `nix develop` does not change directory. So a hook that installs a
// tool installs it into the caller's tree.
func gobinEntries() map[string]bool {
	dir := os.Getenv("GOBIN")
	if dir == "" {
		dir = ".bin"
	}
	names := map[string]bool{}
	items, err := os.ReadDir(dir)
	if err != nil {
		return names // absent is the common case and is not a finding
	}
	for _, it := range items {
		names[it.Name()] = true
	}
	return names
}

// A shell must not ADD to the caller's GOBIN.
//
// GOBLIN-DIV-077: goblin's hook built gopy into $GOBIN whenever it was
// missing. Entering that shell from gapi's checkout therefore wrote a
// gopy into GAPI's .bin, where it led PATH and shadowed the packaged
// one, and gapi's hermeticity gate failed on a binary gapi never asked
// for. Three correct-looking facts composed into it, which is why no
// single review caught it.
//
// Additions fail; REMOVALS are reported and tolerated. That asymmetry is
// deliberate and transitional: gapi's and goblin's hooks now delete a
// stale $GOBIN/gopy on entry, which can only remove an artifact that
// should never have existed. The durable fix is a hook that touches no
// path derived from $PWD at all, at which point the removals go too and
// this can tighten to "no change".
func gobinAdditions(before, after map[string]bool) []string {
	var added []string
	for name := range after {
		if !before[name] {
			added = append(added, name)
		}
	}
	sort.Strings(added)
	return added
}

func shellInventories(shells map[string]string, tools []string) ([]inventory, error) {
	var inv []inventory
	for _, name := range sortedNames(shells) {
		ref := shells[name]
		script := "for t in " + strings.Join(tools, " ") + "; do printf '%s=' \"$t\"; readlink -f \"$(command -v \"$t\")\" || echo MISSING; done"
		beforeBin := gobinEntries()
		out, err := exec.Command("nix", "develop", ref, "--command", "sh", "-c", script).Output()
		if err != nil {
			return nil, fmt.Errorf("entering dev shell %s (%s): %w", name, ref, err)
		}
		if added := gobinAdditions(beforeBin, gobinEntries()); len(added) > 0 {
			return nil, fmt.Errorf(
				"dev shell %s (%s) wrote into THIS repo's GOBIN: %s\n"+
					"  A shell hook must not install into $PWD/.bin: nix does not change\n"+
					"  directory, so $PWD is the caller's checkout and GOBIN leads PATH -\n"+
					"  the installed tool shadows the packaged one in a repo that never\n"+
					"  asked for it. Take the tool from the flake instead (GOBLIN-DIV-077)",
				name, ref, strings.Join(added, ", "))
		}
		values := map[string]string{}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				values[k] = v
			}
		}
		inv = append(inv, inventory{name: name, values: values})
	}
	return inv, nil
}

// CheckShellUnification compares dev shell tool inventories, and
// optionally file-declared module pins, across sibling repositories.
//
// pins is variadic rather than a required parameter so that the four
// repos adopt it as each one grows the file being compared, without a
// signature break rippling through two vendored copies of this library
// on a schedule set by the rename rather than by the work. Passing none
// is exactly today's behaviour.
func CheckShellUnification(shells map[string]string, tools []string, pins ...ModulePins) error {
	inventories, err := shellInventories(shells, tools)
	if err != nil {
		return err
	}
	if len(inventories) < 2 {
		return fmt.Errorf("shell unification check needs at least two shells")
	}

	skew, unresolved := compareInventories(inventories, tools, "not resolvable")
	if len(unresolved) > 0 {
		return fmt.Errorf("dev shell tools not resolvable (add them to the flake, or drop them from the checked list):\n  %s", strings.Join(unresolved, "\n  "))
	}
	if len(skew) > 0 {
		return fmt.Errorf("dev shell tool inventories diverge (converge the flakes and locks):\n  %s", strings.Join(skew, "\n  "))
	}
	fmt.Printf("Shell inventories agree across %d shells for %d tools\n", len(inventories), len(tools))

	for _, p := range pins {
		if err := checkModulePins(p); err != nil {
			return err
		}
	}
	return nil
}

// checkModulePins compares one file-declared pin set across repositories.
func checkModulePins(p ModulePins) error {
	if err := p.validate(); err != nil {
		return err
	}
	inv, err := pinInventories(p)
	if err != nil {
		return err
	}
	skew, unresolved := compareInventories(inv, p.Modules, "not declared")
	if len(unresolved) > 0 {
		return fmt.Errorf("module pins missing from %s (add the require, or drop the module from the checked list):\n  %s", p.File, strings.Join(unresolved, "\n  "))
	}
	if len(skew) > 0 {
		return fmt.Errorf("module pins diverge across %s (converge the require directives and go.sum):\n  %s", p.File, strings.Join(skew, "\n  "))
	}
	fmt.Printf("Module pins agree across %d repos for %d modules in %s\n", len(inv), len(p.Modules), p.File)
	return nil
}
