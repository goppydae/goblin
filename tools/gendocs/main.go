// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Command gendocs renders goblin's reference documentation.
//
// It lives here rather than in the kernel's pkg/docsgen because it needs
// the real command roots, which are per-repo: gapi has its own copy
// calling its own constructors against the same renderers. magelib
// invokes this through DocsConfig.Generators and never learns what a
// cobra command is.
//
// Usage: gendocs [output-root]
//
// The single optional argument is the directory to write beneath, and it
// is magelib's contract with every generator. Under an ordinary
// generation it is "." and output lands in the working tree; under
// `mage docs:check` it is a temporary directory, so the drift gate can
// regenerate and byte-compare without repairing the drift it is
// measuring.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goppydae/gapi/pkg/docsgen"

	"github.com/goppydae/goblin/internal/cli"
)

const productName = "goblin"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

func run() error {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	version, err := repoVersion()
	if err != nil {
		return err
	}

	// goblinctl's root needs no constructor call, and that is a
	// difference from gapi worth stating rather than discovering.
	//
	// cli.RootCmd IS the tree cmd/goblinctl runs - main.go passes this
	// exact variable to gapicli.RunRoot - so walking it cannot diverge
	// from what an operator gets. gapi has no such luxury: its
	// NewGapictlRoot() returns a fresh tree carrying 2 commands against
	// the singleton's 25, because five func init() blocks register the
	// verbs against the package var (GAPI-DIV-117). Generating from the
	// wrong one there would publish a reference missing every operator
	// verb, held steady forever by a drift gate comparing the stub to
	// itself.
	ctl := cli.RootCmd

	// goblind's root IS built by a constructor, which takes the start
	// action as a parameter so internal/cli never imports the supervisor.
	//
	// THE ACTION MUST BE NON-NIL EVEN THOUGH IT IS NEVER RUN. Passing nil
	// leaves `start` with a nil RunE, so cobra reports it as not
	// runnable, IsAvailableCommand() is false, and the generator SILENTLY
	// OMITS IT - measured in gapi, where the first run produced the root
	// and version pages and no start page at all, dropping the daemon's
	// central verb and all of its flags from both the reference and the
	// man pages. Nothing errors; the page set is simply smaller.
	//
	// The rule reaches further than this one call: any group command
	// whose children all lose their Run disappears from the reference
	// too.
	neverRun := func(*cobra.Command, []string) error {
		return fmt.Errorf("gendocs: the documented start action is not executable")
	}
	daemon, _, _ := cli.NewGoblindRoot(neverRun)

	// A slice rather than a map: the order is fixed by the literal, so
	// nothing here depends on map iteration order. Every page is
	// byte-compared by the drift gate, and an unstable walk is the
	// cheapest way to make a gate flap for no reason.
	for _, b := range []struct {
		name string
		cmd  *cobra.Command
	}{
		{"goblinctl", ctl},
		{"goblind", daemon},
	} {
		cliDir := filepath.Join(root, "docs", "content", "reference", "cli", b.name)
		if err := docsgen.CLI(b.cmd, cliDir); err != nil {
			return err
		}
		if err := docsgen.Man(b.cmd, filepath.Join(root, "docs", "man", "man1"), version); err != nil {
			return err
		}
	}

	// Section 7 is the one written page, converted rather than generated.
	// It is NOT skipped when missing: the retired Docs.Man named a source
	// file that did not exist and skipped it in silence, which is how a
	// man page target stayed green for months while producing nothing.
	overview := filepath.Join("docs", "content", "user", "overview.md")
	return docsgen.Overview(overview,
		filepath.Join(root, "docs", "man", "man7", productName+".7"),
		productName, version)
}

// repoVersion reads the VERSION file and nothing else.
//
// Deliberately NOT magelib.Version(), which prefers RELEASE_VERSION and
// the tag ref before falling back to the file. Those make the output a
// function of the ENVIRONMENT, so the same tree would generate different
// man page headers on a tag build than on a branch build - and the drift
// gate, which compares committed bytes against a fresh render, would go
// red on precisely the release commit it most needs to be green on.
//
// Reading the file makes the artifact a function of the tree. The
// consequence is intended and now known rather than predicted: bumping
// VERSION regenerates the man pages, so a bump commit carries them. gapi
// learned that when a VERSION-only pull request turned Sibling Gates red
// with a stale-docs error naming man pages.
func repoVersion() (string, error) {
	// Read from the WORKING TREE, not the output root: the output root is
	// a scratch directory under the drift gate and has no VERSION in it.
	data, err := os.ReadFile("VERSION")
	if err != nil {
		return "", fmt.Errorf("reading VERSION: %w", err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("VERSION is empty; man page headers would carry no version")
	}
	return v, nil
}
