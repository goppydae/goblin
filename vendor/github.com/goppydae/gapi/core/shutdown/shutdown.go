// Package shutdown is the PID-1 teardown executor (GAPI-DIV-027): a
// correct init shutdown is StopAll -> sync -> reverse umount ->
// reboot(2), replacing the naive grace + SIGKILL pattern. The
// privileged surface is an interface so the exact call order is
// asserted in unit tests without rebooting anything.
package shutdown

import (
	"context"
	"log/slog"
	"time"

	"github.com/goppydae/gapi/core/mounts"
	"github.com/goppydae/gapi/internal/logattr"
)

// Action selects the final reboot(2) command.
type Action int

const (
	PowerOff Action = iota
	Reboot
	Halt
)

// Syscalls is the privileged surface: sync(2), umount2(2), reboot(2).
type Syscalls interface {
	Sync()
	Unmount(target string, flags int) error
	Reboot(cmd int) error
}

// AgentStopper is the slice of the agent manager teardown needs.
type AgentStopper interface {
	StopAll() error
}

// SystemShutdown runs the phased teardown. StopAll is bounded by
// grace - a hung agent cannot block sync() - and the deadline derives
// from context.Background(), never an already-cancelled signal
// context. Unmount and StopAll failures are logged, not fatal: the
// machine is going down either way, and the remaining phases only
// make that safer.
func SystemShutdown(sys Syscalls, mgr AgentStopper, mountTable []mounts.MountSpec, action Action, grace time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	stopped := make(chan error, 1)
	go func() { stopped <- mgr.StopAll() }()
	select {
	case err := <-stopped:
		if err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent stop during shutdown failed", logattr.Err(err))
		}
	case <-ctx.Done():
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "agent stop exceeded shutdown grace, proceeding",
			slog.Duration("grace", grace))
	}

	sys.Sync()

	for i := len(mountTable) - 1; i >= 0; i-- {
		if err := sys.Unmount(mountTable[i].Target, mntDetach); err != nil {
			slog.Default().LogAttrs(ctx, slog.LevelWarn, "umount failed during shutdown",
				slog.String("target", mountTable[i].Target), logattr.Err(err))
		}
	}

	switch action {
	case Reboot:
		return sys.Reboot(CmdRestart)
	case Halt:
		return sys.Reboot(CmdHalt)
	default:
		return sys.Reboot(CmdPowerOff)
	}
}
