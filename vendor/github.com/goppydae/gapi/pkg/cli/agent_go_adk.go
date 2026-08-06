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
	"path/filepath"
	"strings"

	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/internal/safeio"
)

// adkModulePath is the module path the shipped ADK source declares, and
// the path the generated main imports the runtime FROM is
// adkModulePath + "/agent".
//
// It is adk/go/AGENT, never adk/go: gopy binds every exported symbol in
// adk/go into the Python ADK's native module, so importing the runtime
// from there would leak it into Python's C bindings.
const adkModulePath = "github.com/goppydae/gapi/adk/go"

// adkImportPath is the Go ADK runtime's import path.
const adkImportPath = adkModulePath + "/agent"

// sharedModulePath is the module the ADK ships AS, and the module the
// generated code resolves adkImportPath inside.
//
// It is the kernel's own module path, not a synthesized one, and that is
// operator decision 38's "share one package" made mechanical. The ADK's
// control channel carries protobuf, so it needs the generated gapi types
// - and a synthesized `adk/go` module cannot import
// github.com/goppydae/gapi/pkg/proto at all. Shipping under a different
// path would leave the checkout tier and the install tier resolving
// DIFFERENT copies of the same generated package, which is a duplicate
// registration into the global protoregistry and a panic at init.
//
// One consequence worth stating because it looks like an omission:
// adkImportPath is UNCHANGED. It always named a path inside this module;
// only the module that owns it stopped being synthesized.
const sharedModulePath = "github.com/goppydae/gapi"

// protobufModulePath is the ADK runtime's one genuine third-party
// dependency, and the only module the stage resolves that this silo does
// not write.
const protobufModulePath = "google.golang.org/protobuf"

// The staged subdirectory names. Named by ROLE, never by the vendor
// (operator decision 2): goblind links this code and its operators have
// never heard of gapi, so a directory called "gapi" would put the
// vendor's name in the kernel's literals for no reader's benefit. The
// module path INSIDE sdk/ is necessarily the real one - that is an
// identity, not prose - and is declared as a wire literal.
const (
	sharedStageDir   = "sdk"
	protobufStageDir = "protobuf"
)

// The three trees inside a shared module root. They are the SAME in a
// checkout and in an install tree, which is the property that removes
// the two-tier asymmetry: an install ships the kernel's layout rather
// than a flattened one, so there is a single set of paths to reason
// about and no tier-specific resolution.
var (
	adkRelDir      = filepath.Join("adk", "go", "agent")
	protoRelDir    = filepath.Join("pkg", "proto")
	protobufRelDir = filepath.Join("vendor", "google.golang.org", "protobuf")
)

// goADK is a located shared module root holding the Go ADK runtime.
//
// Dir is the MODULE ROOT, not the package directory: adkRelDir,
// protoRelDir and protobufRelDir hang off it. GoDirective is the `go`
// line to write into the generated module files, taken from wherever the
// source was found rather than from a constant here: a toolchain version
// hard-coded in the CLI is one that drifts from the source it compiles.
type goADK struct {
	Dir         string
	GoDirective string
	Origin      string
}

// resolveGoADK locates the Go ADK runtime source.
//
// THREE TIERS, IN THE SAME ORDER AND FOR THE SAME REASONS AS
// resolvePyRunner (core/supervisor/lifecycle_handlers.go): an explicit
// override, then the install layout, then the checkout. It differs from
// resolvePyRunner in two respects, both deliberate: the last tier is
// CHECKED rather than returned on faith, so a path that does not exist
// produces a clear error here instead of a `go build` failure whose
// message is about modules and says nothing about the ADK; and that tier
// WALKS UP rather than trusting the working directory to be the
// repository root.
//
// GAPI-DIV-092: before this existed, a Go agent could only be built from
// inside a checkout, while the CLI told every operator otherwise.
func resolveGoADK() (goADK, error) {
	var tried []string

	if v := os.Getenv(product.EnvKey("GO_ADK")); v != "" {
		adk, err := loadGoADK(v, product.EnvKey("GO_ADK"))
		if err != nil {
			// An explicit override that does not resolve is an operator
			// error, not a reason to fall through to a different ADK than
			// the one they named.
			return goADK{}, err
		}
		return adk, nil
	}

	// The install layout: <prefix>/bin/<cli> beside
	// <prefix>/share/<product>/go. The product segment is DERIVED, not
	// spelled: goblind links this code, and a path with the kernel
	// vendor's name in it is exactly what core/product's scan exists to
	// keep out of the kernel (GAPI-DIV-061).
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "..", "share", product.Name(), "go")
		if adk, err := loadGoADK(cand, "install tree"); err == nil {
			return adk, nil
		}
		tried = append(tried, cand)
	}

	// The checkout: adk/go at or ABOVE the working directory.
	//
	// Not a bare "adk/go" relative to the cwd, which assumes the caller
	// stands in the repository root - and nothing makes that true. The
	// ADK harness runs gapictl from test/adk, and a developer building an
	// agent has no reason to be at the top of the tree either.
	//
	// This is where resolvePyRunner's third tier is copied in SHAPE but
	// not in weakness. That tier returns a cwd-relative path on faith; it
	// happens to hit in a dev tree and resolved against / on a booted
	// system, which is how GAPI-DIV-077 stayed invisible in a checkout and
	// was fatal in an image. Walking up removes the assumption instead of
	// relying on where the caller happened to stand.
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; {
			if adk, err := loadGoADK(d, "checkout"); err == nil {
				return adk, nil
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
		tried = append(tried, filepath.Join(wd, adkRelDir)+" (and every parent)")
	}

	return goADK{}, fmt.Errorf(
		"cannot locate the Go ADK source (looked in: %s); set %s to the directory holding agent/*.go",
		strings.Join(tried, ", "), product.EnvKey("GO_ADK"))
}

// loadGoADK validates that dir looks like the ADK source tree and reads
// the go directive out of it.
//
// The validation is deliberately a FILE the package must contain rather
// than the directory merely existing: an empty share/gapi/go, which is
// what a half-finished package install leaves behind, would otherwise be
// accepted and fail later as a compile error in generated code.
func loadGoADK(dir, origin string) (goADK, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return goADK{}, fmt.Errorf("resolve ADK path %s: %w", dir, err)
	}

	if _, err := os.Stat(filepath.Join(abs, adkRelDir, "run.go")); err != nil {
		return goADK{}, fmt.Errorf("%s is not a Go ADK source tree (%s): %w", abs, origin, err)
	}

	// The protobuf runtime is validated as a FILE for the same reason the
	// ADK is: an install that shipped the directory and not its contents
	// would otherwise be accepted here and fail later as a module
	// resolution error in generated code, which names neither the package
	// that is missing nor the install that is incomplete.
	if _, err := os.Stat(filepath.Join(abs, protobufRelDir, "proto", "proto.go")); err != nil {
		return goADK{}, fmt.Errorf(
			"%s ships no protobuf runtime at %s (%s); the ADK's control channel needs it: %w",
			abs, protobufRelDir, origin, err)
	}

	directive, err := goDirectiveFrom(abs)
	if err != nil {
		return goADK{}, err
	}
	return goADK{Dir: abs, GoDirective: directive, Origin: origin}, nil
}

// goDirectiveFrom reads the `go` line from the module that owns dir.
//
// The shipped tree carries its own go.mod. A checkout does not - adk/go
// is part of the kernel module - so the search walks up to the go.mod
// that does own it. Either way the directive comes from the module the
// source was written against.
func goDirectiveFrom(dir string) (string, error) {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
					return strings.TrimSpace(v), nil
				}
			}
			return "", fmt.Errorf("%s declares no go directive", filepath.Join(d, "go.mod"))
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("no go.mod found at or above %s", dir)
		}
		d = parent
	}
}

// assembleGoAgent writes the author's file, the generated main, a copy of
// the ADK source and the two module files into dir, ready to build.
//
// THE STAGE IS SELF-CONTAINED, and that is the whole design (GAPI-DIV-092).
// It carries its own module, and the ADK arrives as a local `replace`
// target rather than as a version to resolve, so `go build` in it touches
// no module proxy and needs no go.sum. The layout:
//
//	dir/go.mod        module agentbuild.local/agent, both replaces
//	dir/agent.go      the author's file
//	dir/main.go       the generated main
//	dir/sdk/          the kernel's module - stageADK owns its interior
//	dir/protobuf/     the protobuf runtime
//
// THE INTERIOR IS stageADK's, NOT THIS FUNCTION'S, and this comment named
// a shape that no longer exists: decision 38 put protobuf on the control
// channel, so the ADK stopped being its own module at dir/adk and became
// the kernel's module under sharedStageDir. A comment describing a
// deleted layout is a claim, and it was false.
//
// This replaced staging the package beside the author's source, which
// worked only inside a checkout that could already resolve the kernel.
// There is now ONE path: a developer in the checkout and an operator who
// installed the package build through exactly the same code, differing
// only in where resolveGoADK found the ADK.
func assembleGoAgent(srcPath, dir string, adk goADK) error {
	d, err := scanGoAgent(srcPath)
	if err != nil {
		return err
	}

	// The author's file becomes agent.go. Its name no longer carries the
	// type because the type has already been read out of it.
	if err := os.WriteFile(filepath.Join(dir, "agent.go"), d.sourceAsMain(), 0600); err != nil {
		return fmt.Errorf("write agent source: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), d.generateMain(adkImportPath), 0600); err != nil {
		return fmt.Errorf("write generated main: %w", err)
	}
	return stageADK(dir, adk)
}

// stageADK copies the shared module and the protobuf runtime into dir,
// and writes the three go.mod files the staged build resolves.
//
// The copy is what makes the provenance stamp cover the whole input.
// HashDirectory walks recursively and mixes each file's relative path in,
// so hashing the stage after this hashes the EXACT bytes that get
// compiled - not a version string that names them, which is all a
// generated `require` would have given. That was the constraint
// GAPI-DIV-092 said routes 1 and 2 must not walk past, and copying
// discharges it without any change to the hashing call.
//
// THE LAYOUT, and every path in it is the kernel's own (decision 38):
//
//	dir/go.mod              module agentbuild.local/agent, both replaces
//	dir/agent.go            the author's file
//	dir/main.go             the generated main
//	dir/sdk/go.mod          module github.com/goppydae/gapi
//	dir/sdk/adk/go/agent/   the ADK runtime
//	dir/sdk/pkg/proto/      the generated types, SHARED with the kernel
//	dir/protobuf/           the protobuf runtime
//
// The directory is sharedStageDir, spelled once as a constant. This
// comment said "gapi" while the code said "sdk" - a doc naming the
// vendor where the code names the role, which is the shape core/product's
// scan exists to catch and does not reach comments.
//
// The stage stays self-contained: `go build` in it resolves both
// replaces locally, touches no proxy, and needs no go.sum. The protobuf
// runtime is COPIED rather than referenced in place, because a replace
// pointing outside the stage would make the stage depend on a path that
// outlives it and would drop the runtime out of the provenance hash.
func stageADK(dir string, adk goADK) error {
	sharedRoot := filepath.Join(dir, sharedStageDir)

	if err := copyGoPackage(filepath.Join(adk.Dir, adkRelDir),
		filepath.Join(sharedRoot, adkRelDir), adk.Dir); err != nil {
		return err
	}
	if err := copyGoPackage(filepath.Join(adk.Dir, protoRelDir),
		filepath.Join(sharedRoot, protoRelDir), adk.Dir); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(adk.Dir, protobufRelDir),
		filepath.Join(dir, protobufStageDir)); err != nil {
		return err
	}

	// v0.0.0 is the conventional placeholder for a requirement that a
	// local replace always satisfies. Nothing resolves it, so no version
	// here can be wrong - and none can be silently right either, which is
	// why identity is carried by the provenance hash and not by this line.
	//
	// The stage's own module path names no product. It is a scratch module
	// that exists for one `go build` and is deleted after, so borrowing the
	// vendor's name for it would put that name in the kernel's literals to
	// no reader's benefit.
	mods := map[string]string{
		filepath.Join(dir, "go.mod"): fmt.Sprintf(
			"module agentbuild.local/agent\n\ngo %s\n\nrequire (\n\t%s v0.0.0\n\t%s v0.0.0\n)\n\nreplace %s => ./%s\n\nreplace %s => ./%s\n",
			adk.GoDirective, sharedModulePath, protobufModulePath,
			sharedModulePath, sharedStageDir, protobufModulePath, protobufStageDir),

		filepath.Join(sharedRoot, "go.mod"): fmt.Sprintf(
			"module %s\n\ngo %s\n\nrequire %s v0.0.0\n",
			sharedModulePath, adk.GoDirective, protobufModulePath),

		filepath.Join(dir, protobufStageDir, "go.mod"): fmt.Sprintf(
			"module %s\n\ngo %s\n",
			protobufModulePath, adk.GoDirective),
	}
	for path, body := range mods {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// copyGoPackage stages one directory of compilable Go source.
//
// Only *.go, and no tests: tests are not compiled into the agent, and
// including them would move the provenance hash for edits that cannot
// affect the binary. An empty result is an error rather than an empty
// package, because a half-finished install is exactly what produces one.
func copyGoPackage(src, dst, origin string) error {
	if err := os.MkdirAll(dst, 0750); err != nil {
		return fmt.Errorf("create stage %s: %w", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}

	var copied int
	for _, e := range entries {
		// The source directory is resolved from an environment variable, so
		// its listing is an untrusted input even though a directory entry
		// cannot contain a separator today. Reducing each name to its base
		// and refusing anything that changes under that leaves no path for
		// an entry to write outside dst.
		name := filepath.Base(e.Name())
		if name != e.Name() {
			return fmt.Errorf("source at %s contains a suspicious entry: %q", src, e.Name())
		}
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := safeio.ReadFileUnder(src, filepath.Join(src, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		out, err := safeio.ResolveUnder(dst, filepath.Join(dst, name))
		if err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		if err := os.WriteFile(out, data, 0600); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
		copied++
	}
	if copied == 0 {
		return fmt.Errorf("%s contains no .go files (from %s)", src, origin)
	}
	return nil
}

// copyTree stages a whole dependency tree, every file, recursively.
//
// NOT filtered to *.go, and that is load-bearing rather than lazy: the
// protobuf runtime go:embeds internal/editiondefaults/editions_defaults.binpb,
// so a *.go-only copy fails the build with "pattern
// editions_defaults.binpb: no matching files found". LICENSE and PATENTS
// travel for the same reason any redistributed source carries them.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		target, terr := safeio.ResolveUnder(dst, filepath.Join(dst, rel))
		if terr != nil {
			return fmt.Errorf("stage %s: %w", rel, terr)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0750)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, derr := safeio.ReadFileUnder(src, path)
		if derr != nil {
			return fmt.Errorf("read %s: %w", rel, derr)
		}
		return os.WriteFile(target, data, 0600)
	})
}
