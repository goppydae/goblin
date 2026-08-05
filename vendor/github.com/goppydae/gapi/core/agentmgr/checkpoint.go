// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/goppydae/gapi/core/checkpoint"
	"github.com/goppydae/gapi/core/procsig"
)

// Checkpoint/restore support for the runners that own a separate
// process (GOBLIN-DIV-018). TimerAgent deliberately does not implement
// lifecycle.Checkpointer: it runs in-process, so there is nothing for
// CRIU to dump, and claiming the capability would make an orchestrator
// schedule a migration that could never complete.

// ErrNoProcess is returned when a checkpoint is requested for a runner
// that has no running process. It is distinct from a CRIU failure: the
// caller's mistake is asking at the wrong time, not a dump that broke.
var ErrNoProcess = errors.New("agentmgr: no running process to checkpoint")

// checkpointProcess dumps pid into dir with the migration-safe options.
//
// LeaveRunning stays false: the image must be a sound rollback point,
// which it stops being the moment the source runs past it.
func checkpointProcess(pid int, dir string) error {
	return checkpoint.Dump(pid, dir, checkpoint.Options{
		ShellJob:       true,
		TCPEstablished: true,
		FileLocks:      true,
	})
}

func restoreProcess(dir string) (int, error) {
	return checkpoint.Restore(dir, checkpoint.Options{
		ShellJob:       true,
		TCPEstablished: true,
		FileLocks:      true,
	})
}

// Checkpoint implements lifecycle.Checkpointer for GoAgent.
func (a *GoAgent) Checkpoint(ctx context.Context, dir string) error {
	_ = ctx // bounds the call only; go-criu offers no cancellation hook
	pid, running := a.Pid()
	if !running {
		return fmt.Errorf("%w: agent %s", ErrNoProcess, a.id)
	}
	return checkpointProcess(pid, dir)
}

// Restore implements lifecycle.Checkpointer for GoAgent.
func (a *GoAgent) Restore(ctx context.Context, dir string) error {
	_ = ctx
	pid, err := restoreProcess(dir)
	if err != nil {
		return err
	}
	return a.adopt(pid)
}

// adopt records a restored process. cmd is cleared because the restored
// process is not the one exec spawned; leaving a stale cmd in place
// would let Stop signal a pid that no longer belongs to this agent -
// exactly the recycling hazard the epoch guard exists to prevent.
//
// The start epoch is captured HERE and stored (GAPI-DIV-046). Reading it
// at Stop time would compare the process to itself and could never fail,
// which makes the guard decorative. Failing to capture it is an error
// the caller must see - a restore that cannot establish the epoch has
// not produced a supervisable process. The pid is still recorded, so the
// reap loop can match the process through Pid(); with no epoch, Stop
// refuses delivery (ErrStaleEpoch) rather than signalling blind - and no
// exit watcher is started either, since there is no epoch to guard the
// liveness poll with.
//
// With an epoch, the exit watcher starts here (GAPI-DIV-048): the claim
// this records has to be able to expire without Stop.
func (a *GoAgent) adopt(pid int) error {
	// Retire any previous watcher first, with the lock released - it
	// takes a.mu, so joining it from under the lock would deadlock.
	a.mu.Lock()
	prev := a.takeAdoptedWatchLocked()
	a.mu.Unlock()
	prev.stop()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd = nil
	a.adoptedPid = pid
	a.adoptedEpoch = 0
	a.stopping = false
	epoch, err := procsig.StartEpoch(pid)
	if err != nil {
		return fmt.Errorf("adopting restored pid %d for agent %s: %w", pid, a.id, err)
	}
	a.adoptedEpoch = epoch
	a.adoptedWatch = newAdoptedWatch(pid, epoch, func() { a.adoptedExited(pid, epoch) })
	return nil
}

// Checkpoint implements lifecycle.Checkpointer for PythonAgent.
func (a *PythonAgent) Checkpoint(ctx context.Context, dir string) error {
	_ = ctx
	pid, running := a.Pid()
	if !running {
		return fmt.Errorf("%w: agent %s", ErrNoProcess, a.id)
	}
	return checkpointProcess(pid, dir)
}

// Restore implements lifecycle.Checkpointer for PythonAgent.
func (a *PythonAgent) Restore(ctx context.Context, dir string) error {
	_ = ctx
	pid, err := restoreProcess(dir)
	if err != nil {
		return err
	}
	return a.adopt(pid)
}

// adopt mirrors GoAgent.adopt, including the epoch capture and the exit
// watcher: the two runners are a parity contract.
func (a *PythonAgent) adopt(pid int) error {
	a.mu.Lock()
	prev := a.takeAdoptedWatchLocked()
	a.mu.Unlock()
	prev.stop()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd = nil
	a.adoptedPid = pid
	a.adoptedEpoch = 0
	a.stopping = false
	epoch, err := procsig.StartEpoch(pid)
	if err != nil {
		return fmt.Errorf("adopting restored pid %d for agent %s: %w", pid, a.id, err)
	}
	a.adoptedEpoch = epoch
	a.adoptedWatch = newAdoptedWatch(pid, epoch, func() { a.adoptedExited(pid, epoch) })
	return nil
}
