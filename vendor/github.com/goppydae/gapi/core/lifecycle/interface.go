// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package lifecycle

import (
	"context"
)

// The agent contract this package once declared lived here as an Agent
// interface returning *adk/meta.AgentInfo. It had no implementors and no
// consumers in either repo, and its single reason to exist was that
// method signature - which is why core/adk/meta was reachable from the
// binaries at all, and why goblin vendored it (GAPI-DIV-082).
//
// The live agent contract is core/agentmgr.Agent. If lifecycle needs its
// own view of an agent later, it should be written against what
// lifecycle actually does with one rather than restored from here.

type Runner interface {
	// Start spawns the runner's process. The context bounds the start
	// operation only - it is cancelled as soon as Start returns, so an
	// implementation must NOT tie the spawned process's lifetime to it.
	// exec.CommandContext here means the process is SIGKILLed the moment
	// the start call completes (GAPI-DIV-028); use exec.Command and let
	// Stop own the process.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Reload(ctx context.Context) error
	Reset()
}

// Optional capability for runners to support per-start correlation.
type RunIDSetter interface {
	SetRunID(string)
}

// SpeechReporter answers whether the current run's child has written any
// control frame at all (GAPI-DIV-104).
//
// A START DEADLINE THAT EXPIRES HAS TWO CAUSES AND THEY NEED DIFFERENT
// ANSWERS. A child that spoke and has not yet reached RUNNING is slow;
// a child that has said nothing is either hung before its first report
// or built against an ADK that never opened the descriptor. Since
// GAPI-DIV-099 the supervisor learns state only from frames the agent
// writes, so silence is the sole evidence for the second case - and
// without this the timeout names neither, reporting only that the wait
// ended.
//
// Optional, on the same terms as RunIDSetter: an in-process runner has
// no control channel and simply does not implement it, which is
// distinct from implementing it and answering false.
type SpeechReporter interface {
	// HasSpoken reports whether any valid control frame has arrived
	// since the current run was started. It is reset by Start, so it
	// answers about this run and not the agent's history.
	HasSpoken() bool
}

// Checkpointer is an optional capability for runners whose process can
// be checkpointed and restored with CRIU (GOBLIN-DIV-018). Runners that
// cannot be dumped - anything running in-process rather than as a
// separate program - simply do not implement it, and an orchestrator
// asserts the capability once at admission rather than discovering at
// migration time that a workload was never movable.
//
// Both methods take a context on the same terms as Start: it bounds the
// call, not the lifetime of the process the call produces.
type Checkpointer interface {
	// Checkpoint writes a checkpoint of the runner's process into dir,
	// which must already exist.
	//
	// On success the process is STOPPED. That is deliberate: the image
	// is the rollback artifact for a migration, and a source that keeps
	// executing past the point its image captured has already diverged
	// from it.
	Checkpoint(ctx context.Context, dir string) error

	// Restore recreates the process from the checkpoint in dir and
	// adopts it, so the runner's Pid reports the restored process and
	// Stop can terminate it.
	//
	// The restored process is not a child of this program - CRIU spawns
	// it - so it is tracked by pid and reparents to the supervisor's
	// subreaper, which is how its eventual exit is observed.
	Restore(ctx context.Context, dir string) error
}
