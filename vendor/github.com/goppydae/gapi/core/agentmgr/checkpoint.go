package agentmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/goppydae/gapi/core/checkpoint"
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
	a.adopt(pid)
	return nil
}

// adopt records a restored process. cmd is cleared because the restored
// process is not the one exec spawned; leaving a stale cmd in place
// would let Stop signal a pid that no longer belongs to this agent -
// exactly the recycling hazard the epoch guard exists to prevent.
func (a *GoAgent) adopt(pid int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd = nil
	a.adoptedPid = pid
	a.stopping = false
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
	a.adopt(pid)
	return nil
}

func (a *PythonAgent) adopt(pid int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmd = nil
	a.adoptedPid = pid
	a.stopping = false
}
