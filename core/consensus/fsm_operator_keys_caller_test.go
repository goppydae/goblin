// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package consensus

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The lock-free read has exactly one permitted caller, and this test is
// what makes that a build failure rather than a comment (GOBLIN-DIV-044).
//
// resolveOperatorKeyLocked takes no lock. It cannot: its caller,
// applyOperatorKeyChange, runs inside FSM.Apply under f.mu.Lock(), and
// sync.RWMutex is not reentrant, so an RLock in the callee would
// deadlock every signed change. The safety of the whole registry read
// surface therefore rests on nobody else naming that method - which is
// exactly the kind of invariant that survives review and dies to the
// next patch. So assert it mechanically.
const (
	lockFreeResolver = "resolveOperatorKeyLocked"
	permittedCaller  = "applyOperatorKeyChange"
)

// TestResolveOperatorKeyLockedHasExactlyOneCaller parses this package
// and fails if the lock-free resolver is named anywhere outside its one
// permitted caller.
//
// It matches every reference, not just call expressions: passing the
// method as a value (as applyOperatorKeyChange does, into
// capability.OperatorKeyResolver) hands the unlocked read to whoever
// holds the closure, so a method value that escapes is the same hazard
// as a direct call.
//
// _test.go files are excluded deliberately. Tests drive the FSM from a
// single goroutine with no Apply running concurrently, so they cannot
// exhibit the race; the invariant being protected is about the shipped
// read paths.
func TestResolveOperatorKeyLockedHasExactlyOneCaller(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}

		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			enclosing := "<package-level declaration>"
			if isFunc {
				enclosing = fn.Name.Name
			}

			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != lockFreeResolver {
					return true
				}
				found++
				if enclosing == permittedCaller {
					return true
				}
				t.Errorf(`%s references %s, which takes NO lock.

%s is safe only because its sole caller, %s, already holds f.mu via
FSM.Apply. You cannot fix this by adding RLock inside it: sync.RWMutex is
not reentrant, so RLock under Apply's write lock deadlocks.

Do one of these instead:
  - reading to AUTHORIZE (resolve a key to check a signature): call
    Consensus.OperatorKeysVerified, which refuses on a non-leader so a
    replica that has not applied a pending remove cannot answer yes;
  - reading state where a stale answer can only fail closed: call
    FSM.OperatorKeysLocal or FSM.OperatorKeyCountLocal, which take the
    read lock, and justify the staleness at the call site;
  - genuinely inside Apply and holding f.mu: add %s to permittedCaller in
    this test and say in the commit why it is Apply-only.`,
					posOf(fset, sel, name, enclosing), lockFreeResolver,
					lockFreeResolver, permittedCaller, enclosing)
				return true
			})
		}
	}

	// A rename or deletion that leaves this test passing vacuously is
	// itself a defect: the check would silently stop guarding anything.
	if found == 0 {
		t.Fatalf("no reference to %s found in this package; "+
			"if it was renamed or removed, update lockFreeResolver in this test "+
			"or delete the test along with the lock-free read it guards",
			lockFreeResolver)
	}
}

// posOf renders a human-locatable site for the failure message.
func posOf(fset *token.FileSet, n ast.Node, file, enclosing string) string {
	return file + ":" + strconv.Itoa(fset.Position(n.Pos()).Line) + " (in " + enclosing + ")"
}
