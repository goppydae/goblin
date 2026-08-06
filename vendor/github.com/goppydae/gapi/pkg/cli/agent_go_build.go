// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/goppydae/gapi/core/crypto"
)

// Compiling a staged Go agent. Split from agent_go_source.go, which
// scans the author's file and assembles the package: this is what turns
// that stage into a signed, stamped binary. The two were one file at 497
// lines, three under the limit, so the split is what the next change to
// either would have forced anyway.

// buildGoAgent assembles the agent into a self-contained stage, compiles
// it into outDir, and returns the binary path and the hash of what was
// compiled.
//
// The stage is a SYSTEM temp directory. It used to sit beside the
// author's file, because the assembled package imported the ADK by module
// path and staging outside the module tree would have needed a resolvable
// kernel version. assembleGoAgent now brings the ADK with it, so the
// stage no longer needs to be inside anything - which is what makes this
// work for an operator who installed gapi rather than cloning it
// (GAPI-DIV-092), and stops the build writing scratch directories into
// the author's source tree on the way.
func buildGoAgent(srcPath, outDir string) (string, string, error) {
	// Checked before any staging so the message names the missing thing.
	// The package ships the ADK but deliberately not a compiler, so this
	// is the one prerequisite an installed operator has to supply, and it
	// should say so rather than arrive as a failed `go build`.
	if _, err := exec.LookPath("go"); err != nil {
		return "", "", fmt.Errorf("building a Go agent needs the go toolchain on PATH: %w", err)
	}

	adk, err := resolveGoADK()
	if err != nil {
		return "", "", err
	}

	stage, err := os.MkdirTemp("", "agent-build-")
	if err != nil {
		return "", "", fmt.Errorf("stage dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if err := assembleGoAgent(srcPath, stage, adk); err != nil {
		return "", "", err
	}

	// Hash the STAGED package, not the author's file alone: the generated
	// main and the staged ADK are compiled into the binary too, so a
	// provenance hash that ignored them would certify an input that is not
	// the whole input. HashDirectory walks recursively, so the ADK copy
	// under adk/agent is covered without a second call.
	//
	// EVERY FILE, not "*.go". The stage stopped being all-Go the moment it
	// carried a vendored dependency: protobuf go:embeds
	// editions_defaults.binpb, which is COMPILED IN and which a .go
	// pattern does not see. REPRODUCED: mutating that file left the hash
	// byte-identical, so the stamp certified less than it compiled. A
	// pattern needing revision whenever the staged input gains a file type
	// is a pattern that will be wrong again - and the stage is assembled
	// by this program, so everything in it is compiled input by
	// construction (GAPI-DIV-103).
	sourceHash, err := crypto.HashDirectory(stage, "*")
	if err != nil {
		return "", "", fmt.Errorf("hash assembled package: %w", err)
	}

	if err := os.MkdirAll(outDir, 0750); err != nil {
		return "", "", fmt.Errorf("create output directory: %w", err)
	}
	outBinary, err := filepath.Abs(filepath.Join(outDir, goAgentArtifact(srcPath)))
	if err != nil {
		return "", "", fmt.Errorf("resolve output path: %w", err)
	}

	// THE STAMP TARGETS A VARIABLE THAT EXISTS (GAPI-DIV-103, operator
	// decision 44). This read `-X main.SourceHash=...` while the
	// generated main declared no such variable, and `-X` for a missing
	// symbol is dropped without a word. adkImportPath is the constant
	// the stage's replace and the generated import already use, so the
	// flag cannot drift from the package it names.
	//
	// BuildTime is GONE, not moved: a wall-clock stamp and a
	// byte-reproducible build are mutually exclusive, and it was dead
	// too.
	ldflags := fmt.Sprintf("-X %s.SourceHash=%s", adkImportPath, sourceHash)

	// -trimpath keeps the stage's path out of the binary. The stage is a
	// fresh MkdirTemp per build and its name was embedded 107 times, so
	// two builds of one tree differed by 3,272,435 bytes - that, not the
	// timestamp the ledger blamed, is what moved the .b3 every time.
	build := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", outBinary, ".")
	build.Dir = stage
	// The stage resolves everything locally, so the build is pinned to say
	// so. GOPROXY=off turns "this needed the network" from a slow success
	// on a connected host into an immediate, legible failure everywhere -
	// which is the property GAPI-DIV-092 chose this route for, and it is
	// worth nothing if it is only true on the machines that test it.
	// GOWORK=off and -mod=mod stop an inherited workspace or vendor mode
	// from reaching in; the stage belongs to no workspace and has no
	// vendor directory.
	cgo := stagedCGOEnv()
	if err := preflightCGO(cgo); err != nil {
		return "", "", err
	}
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off", cgo)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return "", "", fmt.Errorf("build %s: %w", filepath.Base(srcPath), err)
	}

	return outBinary, sourceHash, nil
}
