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

	// EnvForceDummy selects the stub ADK without warning, for tests and
	// for hosts with no native ADK build.
	EnvForceDummy = "ADK_FORCE_DUMMY"

	// EnvRejectDummy makes falling back to the stub ADK a hard failure.
	// Set by the supervisor in production mode.
	EnvRejectDummy = "ADK_REJECT_DUMMY"
)
