// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	gapiproduct "github.com/goppydae/gapi/core/product"
)

// GOBLIN-DIV-073. goblin never adopted the table GAPI-DIV-071 built for
// it, so its control port lived as two local literals - a const here and
// the --listen-addr default in goblind.go - while the kernel carried a
// third in controlAddrDefaults.
//
// THE ENTRY'S DIAGNOSIS WAS WRONG IN A WAY THAT UNDERSTATED IT. It filed
// the kernel's "goblin" entry as a value produced and never read.
// goblinctl mounts the kernel's verbs under `agent`, and those resolve
// through the kernel's config loader, so that entry was read on one code
// path while the local literal served the other. MEASURED before fixing,
// by moving the table entry to 29777 and building in vendor mode:
//
//	goblinctl agent ping     -> dialled 127.0.0.1:29777
//	goblinctl cluster status -> failed to dial 127.0.0.1:29000
//
// One binary, two answers, from one invocation of the same operator. They
// agreed only because two independent literals happened to hold the same
// string, which is precisely what breaks the moment operator decision 47
// moves goblin's port to 31415.

// TestControlAddrDefaultsComeFromTheKernel is the assertion two
// independent literals could not pass: both binaries must resolve the
// same address, and it must be the kernel's.
func TestControlAddrDefaultsComeFromTheKernel(t *testing.T) {
	want := gapiproduct.DefaultControlAddr()

	t.Run("goblind listen-addr default", func(t *testing.T) {
		root, _, _ := NewGoblindRoot(func(*cobra.Command, []string) error { return nil })
		startCmd, _, err := root.Find([]string{"start"})
		if err != nil {
			t.Fatalf("goblind root has no start subcommand: %v", err)
		}
		f := startCmd.Flags().Lookup("listen-addr")
		if f == nil {
			t.Fatal("goblind start has no --listen-addr flag")
		}
		if f.DefValue != want {
			t.Errorf("goblind --listen-addr default = %q, want %q "+
				"(core/product.DefaultControlAddr for %q)", f.DefValue, want, gapiproduct.Name())
		}
	})

	// The other half of the split. goblinctl's own verbs resolve through
	// controlAddr(); the kernel verbs mounted under `agent` resolve
	// through the kernel's loader. Both must land on the same address.
	t.Run("goblinctl empty control-addr", func(t *testing.T) {
		saved := controlFlags.ControlAddr
		t.Cleanup(func() { controlFlags.ControlAddr = saved })
		controlFlags.ControlAddr = ""

		if got := defaultControlAddr(); got != want {
			t.Errorf("goblinctl's built-in default = %q, want %q", got, want)
		}
	})
}

// hostPort matches an address literal with a port. Applied to STRING
// LITERALS FROM THE AST rather than to file text, deliberately: this
// package's comments quote historical dial failures verbatim
// ("failed to dial 127.0.0.1:29000"), and those are records of what went
// wrong, not declarations that can drift. A textual grep would either
// flag them or be loosened until it flagged nothing.
var hostPort = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]{2,5}$|^localhost:[0-9]{2,5}$`)

// TestNoControlPortLiteralInPackage is the standing half of the entry:
// consolidation that is not enforced is consolidation that gets undone by
// the next person who needs a default in a hurry.
func TestNoControlPortLiteralInPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Test files are exempt: a test that dials a specific port is
		// constructing a situation, not declaring a default. The
		// production files are where a default can hide.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if hostPort.MatchString(v) {
				t.Errorf("%s: address literal %q at %s. The control default "+
					"comes from core/product.DefaultControlAddr; a literal here "+
					"is the second declaration GOBLIN-DIV-073 removed.",
					name, v, fset.Position(lit.Pos()))
			}
			return true
		})
	}

	// A scan that silently examined nothing reads exactly like a pass.
	if scanned == 0 {
		t.Fatal("scanned no source files; this gate is not gating")
	}
	t.Logf("scanned %d non-test files in %s", scanned, mustAbs(t, "."))
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("resolving %s: %v", p, err)
	}
	return a
}
