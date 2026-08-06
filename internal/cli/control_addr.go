// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	gapiproduct "github.com/goppydae/gapi/core/product"
)

// This file exists because cli.go was at 486 lines against a 500-line
// gate, and the change below needs its reasoning written down. A file
// with fourteen lines of headroom cannot carry an explanation, and
// compressing the explanation to fit the file is the wrong trade - the
// gate is there to force a split, not a shorter comment.

// defaultControlAddr is goblind's default listen address, and the
// INTERPRETATION of empty lives in goblin because the shared
// --control-addr default is EMPTY.
//
// The contract gives that empty default the meaning "resolve from
// config", which keeps config the source of truth and the flag an
// override. gapictl can honour that; goblinctl cannot, because goblin
// has no config package at all - there is nothing to resolve from. So
// empty resolves to the value below, and the flag's NAME, shorthand and
// default still come from one definition as the contract requires. Only
// the interpretation of empty differs, and it differs because one binary
// has a config loader and the other does not. Recorded as a residual on
// GOBLIN-DIV-052 rather than papered over: a config source for goblinctl
// is its own piece of work.
//
// THE MEANING OF EMPTY IS LOCAL; THE VALUE IS NOT. GOBLIN-DIV-073: this
// was a literal, so goblin declared its control port twice locally while
// the kernel shipped a third declaration in controlAddrDefaults. The
// entry filed that third one as a value nothing read, AND THAT WAS
// WRONG in the direction that understated it - goblinctl mounts the
// kernel's verbs under `agent`, and those resolve through the kernel's
// config loader, so the table was read on that path and the local
// literal on this one. One binary, two answers. MEASURED before fixing,
// with the table's goblin entry moved to 29777 and a vendor-mode build:
// `goblinctl agent ping` dialled 29777 while `goblinctl cluster status`
// dialled 29000. They agreed until something moved, which is what
// operator decision 47 is about to do.
//
// A FUNCTION RATHER THAN A VAR, deliberately. RootCmd sets the product
// during package initialization, and a package-level var would make this
// read depend on initialization order across files in this package.
// DefaultControlAddr panics on an unset identity rather than guessing
// another product's port, so that hazard surfaces as a startup panic
// rather than a daemon quietly binding the wrong one.
func defaultControlAddr() string { return gapiproduct.DefaultControlAddr() }
