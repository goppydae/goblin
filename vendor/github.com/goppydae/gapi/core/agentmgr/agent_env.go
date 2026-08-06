// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

// The kernel-to-agent environment contract.
//
// These names are FIXED and carry no product prefix, which is the one
// deliberate exception to "each project owns one prefix"
// (cli-contract.md). They were GAPI_RUN_ID, GAPI_FORCE_DUMMY_ADK and
// GAPI_REJECT_DUMMY_ADK, which named the kernel to an agent author who
// has no reason to know the kernel exists - the same defect as the
// operator-facing names, one layer down (GAPI-DIV-061).
//
// They do not follow the product identity, and that is the point. The
// reader is the ADK - adk/python/agent/runner.py and its Go peer -
// which ships with the kernel and is the same code whichever daemon
// spawned the process. Namespacing these by product would mean the
// runner could not know its own variable's name until it had read some
// other variable to find out, so the bootstrap would need a fixed name
// anyway; ADK_ IS that fixed name, with nothing composed on top.
//
// A host running both daemons does not collide here: these are set on
// the CHILD's environment by exec, never exported process-wide.
const (
	// EnvRunID correlates one agent start with the events it emits.
	EnvRunID = "ADK_RUN_ID"

	// EnvRejectDummy makes falling back to the stub ADK a hard failure.
	// Set by the supervisor in production mode, and by discovery on the
	// same condition (GAPI-DIV-086) - the two must answer "is a host with
	// no native build a supported deployment" identically.
	//
	// ADK_FORCE_DUMMY IS GONE (operator decision, 2026-08-06). It selected
	// the stub deliberately, and its own doc string here said it was "for
	// tests and for hosts with no native ADK build" - which was the
	// ambiguity -086 existed to remove, preserved in the constant that
	// created it. Decision 30 had already settled that a host with no
	// native build is not a supported deployment.
	//
	// Setting it alongside this variable also killed the process outright:
	// FORCE raised the ImportError and REJECT caught it and exited 1. That
	// looked like a precedence question needing an answer. It was not one.
	// The stub was only load-bearing because it was the sole Python ADK
	// whose events the supervisor could hear (GAPI-DIV-099), and it stopped
	// being that; nothing legitimate needed to force it afterwards. The
	// stub remains reachable the honest way - by having no extension built
	// - which is a state the runner still warns about.
	EnvRejectDummy = "ADK_REJECT_DUMMY"

	// EnvControlFD names the inherited descriptor an agent writes its
	// typed lifecycle frames to (operator decisions 37 and 38).
	//
	// THE VALUE IS A DESCRIPTOR NUMBER, not a count - unlike LISTEN_FDS,
	// which is a count whose descriptors start at 3. The control
	// descriptor is passed AFTER any listeners precisely so systemd's
	// convention is left alone: an agent that also has sockets finds
	// them exactly where it always did, and finds this one wherever this
	// variable says.
	EnvControlFD = "ADK_CONTROL_FD"
)
