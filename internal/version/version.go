// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package version carries goblin's own build-time version string and
// nothing else.
//
// It does NOT mirror gapi's core/version, though it claimed to until
// GOBLIN-DIV-052: that package holds the renderer, the build metadata
// and the active-binary identity, and this one holds a single string.
// Mirroring the name while sharing none of the shape is what let both
// Goblin binaries print cobra's anonymous one-liner while appearing to
// have a version story.
//
// The renderer is the kernel's, deliberately - one implementation for
// all four binaries (cli-contract.md). Goblin's roots pass the string
// below to gapi/pkg/cli's constructors, which register it through
// core/version.SetBinaryNameAndVersion, so a Goblin binary reports both
// its own version and the version of the kernel it embeds.
//
// The root VERSION file is the single source of version truth;
// magelib.Version() resolves it at build time.
package version

// Version is injected at build time via
// -ldflags "-X github.com/goppydae/goblin/internal/version.Version=<v>".
//
// Commit, build date, built-by, Go version and platform are NOT here.
// They live in the kernel's core/version and reach the shared block from
// there; a field would belong here only if goblin's build injected a
// value the kernel's could not.
var Version = "dev"
