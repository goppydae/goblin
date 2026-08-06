// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package adkpath locates the Python ADK on the system it is running on.
//
// It exists because two callers need the same answer and neither can
// import the other: the daemon starts and describes agents through the
// runner, and the CLI prints the command an operator should run. A
// second copy of this resolution is a second set of answers, and
// GAPI-DIV-093 is what that looked like - the CLI printed a
// checkout-relative path on an installed system, where it resolves to
// nothing.
package adkpath

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goppydae/gapi/core/product"
)

// runnerRel is the runner's location WITHIN the ADK tree. It is the one
// path segment that is structural rather than resolved.
var runnerRel = filepath.Join("agent", "runner.py")

// PyADK is a located Python ADK tree.
//
// THE ROOT IS THE THING BEING SELECTED, not the runner file, and that is
// why this carries both (GAPI-DIV-109). runner.py does
// sys.path.append(dirname(__file__) + "/..") and then imports
// gapi.native from it, so pointing at the script silently chooses which
// gapi package - stub or native - gets imported. A variable naming a
// leaf cannot express "use this ADK" while deciding exactly that, which
// is the confusion behind GAPI-DIV-085, -086 and operator decisions 30
// and 41.
type PyADK struct {
	// Root is the tree containing agent/ and gapi/.
	Root string
	// Runner is the path to execute; always Root/agent/runner.py.
	Runner string
	// Source names the tier that produced this, for error messages and
	// for the CLI to say where it looked.
	Source string
}

// load validates that dir is a Python ADK tree and returns it.
//
// CHECKED, NOT ASSUMED. The tier this replaces returned a
// cwd-relative path on faith, which resolved against / on a booted
// system - invisible in a checkout and fatal in an image (GAPI-DIV-077).
// The path is made absolute BEFORE it is used, exactly as loadGoADK
// does. That is not only for gosec's taint analysis, though G703 is what
// names it: an error about a relative path tells an operator nothing
// about which directory it was relative to, and this resolver exists
// because a relative path was trusted.
func load(dir, source string) (PyADK, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return PyADK{}, fmt.Errorf("%s: resolve %s: %w", source, dir, err)
	}
	runner := filepath.Join(abs, runnerRel)
	if _, err := os.Stat(runner); err != nil {
		return PyADK{}, fmt.Errorf("%s: no Python ADK at %s: %w", source, abs, err)
	}
	return PyADK{Root: abs, Runner: runner, Source: source}, nil
}

// ResolvePyADK locates the Python ADK tree.
//
// THREE TIERS, IN THE SAME ORDER AND FOR THE SAME REASONS AS
// pkg/cli.resolveGoADK: an explicit override, then the install layout,
// then the checkout. The two are deliberately the same shape, because
// the previous asymmetry was the defect - the Go side walked up and
// checked every tier while the Python side trusted the working
// directory and looked for the install tree in a place no layout ever
// put it.
func ResolvePyADK() (PyADK, error) {
	exe, _ := os.Executable()
	wd, _ := os.Getwd()
	return resolve(os.Getenv(product.EnvKey("PY_ADK")), exe, wd)
}

// resolve is ResolvePyADK's testable body. The executable path and the
// working directory are ARGUMENTS because the install tier cannot
// otherwise be exercised: os.Executable reports the test binary, so a
// test of that tier would be a test of wherever `go test` happened to
// put it. The tier that could not be unit tested is the tier that was
// wrong for the life of the project.
func resolve(override, exe, wd string) (PyADK, error) {
	var tried []string

	if override != "" {
		// An explicit override that does not resolve is an operator
		// error, not a reason to fall through to a different ADK than
		// the one they named.
		return load(override, product.EnvKey("PY_ADK"))
	}

	// The install layout: <prefix>/bin/<binary> beside
	// <prefix>/share/<product>/python. The product segment is DERIVED,
	// not spelled, so the kernel never writes the vendor's name into a
	// literal (operator decision 2, GAPI-DIV-061).
	//
	// THE OLD TIER LOOKED UNDER <exedir>/adk/python, WHICH NO LAYOUT
	// PRODUCES. Verified against the store path: the package installs to
	// share/<product>/python, so this tier could never hit and the
	// daemon's Python support rested entirely on the wrapper's env
	// default.
	if exe != "" {
		cand := filepath.Join(filepath.Dir(exe), "..", "share", product.Name(), "python")
		if adk, lerr := load(cand, "install tree"); lerr == nil {
			return adk, nil
		}
		tried = append(tried, cand)
	}

	// The checkout: adk/python at or ABOVE the working directory, for
	// the same reason resolveGoADK walks - nothing makes the caller
	// stand in the repository root, and the ADK harness and a developer
	// building an agent both run from somewhere else.
	if wd != "" {
		for d := wd; ; {
			cand := filepath.Join(d, "adk", "python")
			if adk, lerr := load(cand, "checkout"); lerr == nil {
				return adk, nil
			}
			parent := filepath.Dir(d)
			if parent == d {
				tried = append(tried, filepath.Join(wd, "adk", "python")+" (and every parent)")
				break
			}
			d = parent
		}
	}

	return PyADK{}, fmt.Errorf(
		"no Python ADK found; set %s to its directory. Looked in: %v",
		product.EnvKey("PY_ADK"), tried)
}
