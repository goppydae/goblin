// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/goppydae/goblin/internal/cli"
)

// This is GOBLIN-DIV-056's gate: no operator-facing string a goblin
// binary produces names the kernel.
//
// It walks the LIVE command tree rather than scanning source, and that
// choice is what makes it worth having. goblinctl mounts gapictl's whole
// verb tree under `agent`, so half the strings an operator reads out of
// goblinctl are compiled from the vendored kernel and appear in no goblin
// file at all. A source scan of internal/ would have reported this
// property satisfied while `goblinctl agent ping --help` printed
// "Ping gapid".
//
// That is also why this gate could not go green before the re-vendor. It
// depends on GAPI-DIV-061 having fixed gapi's own prose - which is the
// honest shape of the dependency the two entries recorded in each other.

var vendorRe = regexp.MustCompile(`(?i)\bgapi`)

// walkCommands visits every command in the tree, depth first.
func walkCommands(root *cobra.Command, visit func(*cobra.Command)) {
	visit(root)
	for _, c := range root.Commands() {
		walkCommands(c, visit)
	}
}

// No cobra Use, Short, Long, Example or flag usage string names gapi.
func TestCommandTreeDoesNotNameTheKernel(t *testing.T) {
	var offenders []string

	check := func(path, field, val string) {
		if val != "" && vendorRe.MatchString(val) {
			// Collapse to one line so a multi-line Long is readable in
			// the failure rather than swamping it.
			flat := strings.Join(strings.Fields(val), " ")
			if len(flat) > 120 {
				flat = flat[:117] + "..."
			}
			offenders = append(offenders, path+" ."+field+": "+flat)
		}
	}

	// BOTH roots. goblinctl's is the package singleton; goblind's has to
	// be constructed, and skipping it was a real hole - a sabotage of
	// goblind's --listen-addr usage was caught only by the literal scan
	// below, which cannot see the vendored kernel. A gapi-named string
	// arriving from gapicli.RegisterDaemonFlags would have been caught by
	// neither gate.
	goblindRoot, _, _ := cli.NewGoblindRoot(func(*cobra.Command, []string) error { return nil })

	for _, root := range []*cobra.Command{cli.RootCmd, goblindRoot} {
		walkCommands(root, func(c *cobra.Command) {
			p := c.CommandPath()
			check(p, "Use", c.Use)
			check(p, "Short", c.Short)
			check(p, "Long", c.Long)
			check(p, "Example", c.Example)
			c.Flags().VisitAll(func(f *pflag.Flag) { check(p, "flag --"+f.Name, f.Usage) })
			c.PersistentFlags().VisitAll(func(f *pflag.Flag) { check(p, "persistent --"+f.Name, f.Usage) })
		})
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("operator-facing command strings name the kernel.\n"+
			"An operator running goblind installed one product, and gapi is a "+
			"library inside it - describe what the thing DOES, or name goblin's "+
			"own verb (GOBLIN-DIV-056). A string sourced from the vendored kernel "+
			"is fixed in gapi under GAPI-DIV-061, not here:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// allowedLiterals are file-scoped exact values that may name the kernel,
// with the reason. File-scoped rather than global for the same reason
// gapi's equivalent is: a blanket "gapictl is fine" would cover both the
// string that ERASES the name and any string that displays it.
//
// It shrinks or fails - an entry matching nothing is reported below, so
// fixing a line forces its exemption to be deleted.
var allowedLiterals = map[string][]string{
	"internal/cli/cli.go": {
		// rebrandMounted's search term. This literal exists precisely so
		// the name never reaches an operator; forbidding it would forbid
		// the fix for the thing this gate checks.
		"gapictl ",
	},
}

func literalAllowed(file, val string) bool {
	for _, v := range allowedLiterals[file] {
		if v == val {
			return true
		}
	}
	return false
}

// No error or log message literal in goblin's own tree names gapi.
//
// The command tree above cannot see these - they are produced at runtime,
// not registered on a cobra command - so this is a source scan. It parses
// the AST rather than matching quotes in raw text, which is not fussiness:
// a raw scan reads COMMENTS, so the comment explaining why a bad default
// was replaced trips the gate that proves it was replaced. Import paths
// and struct tags are string literals too and are excluded structurally.
//
// The residual the entry records still holds: a message composed at
// runtime from a variable is invisible here.
func TestMessageLiteralsDoNotNameTheKernel(t *testing.T) {
	var offenders []string
	seenAllowed := map[string]bool{}
	fset := token.NewFileSet()

	walkTree(t, func(rel, body string) {
		if filepath.Ext(rel) != ".go" || strings.HasSuffix(rel, "_test.go") {
			return
		}
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") &&
			!strings.HasPrefix(rel, "core/") {
			return
		}
		f, err := parser.ParseFile(fset, rel, body, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		skip := map[*ast.BasicLit]bool{}
		for _, spec := range f.Imports {
			skip[spec.Path] = true
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if fld, ok := n.(*ast.Field); ok && fld.Tag != nil {
				skip[fld.Tag] = true
			}
			b, ok := n.(*ast.BasicLit)
			if !ok || b.Kind != token.STRING || skip[b] {
				return true
			}
			val, err := strconv.Unquote(b.Value)
			if err != nil || !vendorRe.MatchString(val) {
				return true
			}
			if literalAllowed(rel, val) {
				seenAllowed[rel+"\x00"+val] = true
				return true
			}
			offenders = append(offenders,
				rel+":"+strconv.Itoa(fset.Position(b.Pos()).Line)+": "+strconv.Quote(val))
			return true
		})
	})

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("message literals in goblin's tree name the kernel.\n"+
			"These reach an operator through logs and errors, where the kernel "+
			"is not a thing they installed (GOBLIN-DIV-056):\n  %s",
			strings.Join(dedupe(offenders), "\n  "))
	}

	// A stale exemption is worse than none: it claims a defect is
	// declared when the line it named is gone.
	var stale []string
	for file, vals := range allowedLiterals {
		for _, v := range vals {
			if !seenAllowed[file+"\x00"+v] {
				stale = append(stale, file+": "+strconv.Quote(v))
			}
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("allowedLiterals entries match nothing - delete them:\n  %s",
			strings.Join(stale, "\n  "))
	}
}
