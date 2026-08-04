// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build cluster

package main_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// GOBLIN-DIV-059's fix is a WAIT, and a wait is invisible when it works.
//
// TestTwoNodeLiveMigration is correct only because it blocks on
// NodeRPC.MigrationReady - the same pre-flight the coordinator runs -
// before asking for the migration. Delete that one line and the test still
// compiles, still passes most of the time, and races exactly as it did
// before the entry was filed. Nothing in the suite would notice.
//
// This is the residual the entry recorded and could not close with a
// behavioural test: you cannot assert "the race did not happen" without
// reproducing the race. So assert the structure instead.
//
// This file is tagged `cluster`, NOT `cluster && criu`. Parsing a file
// does not require the tags that file is gated by, so this guard runs in
// the ordinary cluster suite on every change, while the test it guards
// runs only inside the NixOS VM check. A guard that runs more often than
// the thing it guards is the point.
const (
	readinessWait = "waitMigrationReady"
	waitingTest   = "TestTwoNodeLiveMigration"
	waitingSource = "migration_test.go"
)

func TestLiveMigrationStillWaitsForReadiness(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, waitingSource, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", waitingSource, err)
	}

	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == waitingTest {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatalf("%s declares no %s.\n\n"+
			"If it was renamed, update waitingTest in this file. If it was "+
			"deleted, GOBLIN-DIV-059's close condition no longer has a subject "+
			"and the entry needs reopening, not this guard removing.",
			waitingSource, waitingTest)
	}

	found := false
	ast.Inspect(target, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == readinessWait {
			found = true
		}
		return !found
	})

	if !found {
		t.Errorf(`%s no longer calls %s.

That call is GOBLIN-DIV-059's entire fix. Without it the test asserts
against a destination node that may not have applied the operator-key
registry yet, which is the race the entry was filed for - and it fails
intermittently, under load, in CI, not here.

Restore the call. If the readiness pre-flight genuinely moved somewhere
else, update readinessWait in this file and say in the commit where it
went.`, waitingTest, readinessWait)
	}
}
