// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// GOBLIN-DIV-062 is blocked on a fourth occurrence of an mTLS admission
// hang, and the accept-loop slog capture is the only thing that will name
// its cause when it arrives. Silence in that capture is itself the
// diagnosis - it means the connection was never accepted, a different
// defect from being accepted and refused.
//
// Every reference to the capture lives inside one file, so dropping the
// call compiles, passes, and destroys the evidence path without a signal.
// The wait would then restart from zero. Assert the wiring mechanically.
const (
	captureHelper  = "captureAcceptLog"
	capturedTest   = "TestSharedListener_AdmitsRaftPeerWithIssuedCert"
	capturedSource = "mtls_raft_test.go"
)

func TestAdmissionTestStillCapturesTheAcceptLog(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, capturedSource, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", capturedSource, err)
	}

	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == capturedTest {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatalf("%s declares no %s.\n\n"+
			"If the test was renamed, update capturedTest in this file. If it "+
			"was deleted, GOBLIN-DIV-062 lost its evidence path and the entry "+
			"needs amending before this guard is removed.",
			capturedSource, capturedTest)
	}

	found := false
	ast.Inspect(target, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == captureHelper {
			found = true
		}
		return !found
	})

	if !found {
		t.Errorf(`%s no longer references %s.

GOBLIN-DIV-062 is waiting on a fourth real occurrence, and that capture is
the only thing that will name the cause when it happens - including by
being SILENT, which means the connection was never accepted at all.

Restore the call, or amend GOBLIN-DIV-062 to record that the evidence path
was deliberately removed and what replaced it. Do not delete this guard to
make the suite green.`, capturedTest, captureHelper)
	}
}
