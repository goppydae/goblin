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
)

// The staged build's cgo policy, split out of agent_go_source.go when it
// pushed that file past the 500-line limit. The split is along a real
// seam rather than at a convenient line: this file decides what BUILD
// ENVIRONMENT an agent is compiled in, and agent_go_source.go decides
// what SOURCE goes into it.

// stagedCGOEnv resolves CGO_ENABLED for the staged build, and returns the
// setting that will WIN rather than the one that was asked for - exec
// gives the last duplicate key priority, so this is appended after
// os.Environ() and is the effective value.
//
// GAPI-DIV-105: `gapictl agent build` required a C compiler on a stock
// host and said so only in the toolchain's own words. CGO_ENABLED
// defaults to 1 and adk/go/agent imports `net`, so the staged build
// pulled in runtime/cgo and died with `cgo: C compiler "gcc" not found`
// on a machine that had a Go toolchain and no gcc. The reason no test saw
// it is the reason it mattered: every test that builds an agent runs
// inside `nix develop`, where a C compiler is always present, so the
// suite was blind to the one configuration an installed operator is most
// likely to have.
//
// THREE LEVELS, most explicit first:
//
//   - --cgo (or --cgo=false) is the operator saying it outright;
//   - an inherited CGO_ENABLED is the operator saying it for the shell;
//   - otherwise 0, because nothing in the ADK runtime needs cgo and the
//     pure-Go resolver is the right one for a supervised process.
//
// The default is what removes the unstated requirement. Preflighting the
// compiler instead would only have turned an obscure failure into a clear
// one; it would not have removed the need for gcc on every host that
// builds an agent.
func stagedCGOEnv() string {
	if cgoFlag != nil {
		if *cgoFlag {
			return "CGO_ENABLED=1"
		}
		return "CGO_ENABLED=0"
	}
	// Named rather than left to os.Environ() passing it through. The
	// pass-through would behave identically; spelling it out is what makes
	// the precedence a thing a reader can see and a test can assert.
	//
	// An EMPTY value is treated as unset. `CGO_ENABLED=` is not a choice
	// anyone makes on purpose - it is what an unset shell variable expands
	// to - and passing it through would hand the toolchain a value it
	// rejects, turning a defaulted build into a failed one.
	if v, ok := os.LookupEnv("CGO_ENABLED"); ok && v != "" {
		return "CGO_ENABLED=" + v
	}
	return "CGO_ENABLED=0"
}

// preflightCGO reports a missing C compiler BEFORE the toolchain reports
// it obscurely, and only when cgo is actually going to be used.
//
// This is the other half of GAPI-DIV-105's exit. The CLI already refuses a
// missing Go toolchain by name rather than letting it arrive as a failed
// build; what the toolchain in turn requires is held to the same standard.
// It cannot fire on the default path, because the default needs no
// compiler - it exists for the operator who asked for cgo back.
func preflightCGO(cgoEnv string) error {
	if cgoEnv != "CGO_ENABLED=1" {
		return nil
	}
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "gcc"
	}
	if _, err := exec.LookPath(cc); err != nil {
		return fmt.Errorf("building with cgo needs a C compiler on PATH: %q not found. "+
			"Install one, set CC to the compiler you want, or drop --cgo - the "+
			"default builds without cgo and needs no C compiler: %w", cc, err)
	}
	return nil
}
