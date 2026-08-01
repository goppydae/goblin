package supervisor

import (
	"context"

	"github.com/goppydae/gapi/core/mounts"
)

// Phase0Deps are the pre-userspace obligations, injected so the boot
// ordering is unit-testable without touching real syscalls; the wiring
// to the concrete kernel packages lives in the daemon entrypoint, and
// the real integration is proven by the PID-1 container e2e.
type Phase0Deps struct {
	// RequirePidfd asserts the kernel can pin a process with
	// pidfd_open. It runs before everything else because it is a
	// precondition rather than an obligation: without pidfd, os/exec
	// silently falls back to signalling by bare PID and every later
	// step is built on a guarantee that is not there (GAPI-DIV-016).
	RequirePidfd func() error
	// Subreaper marks this process as a child subreaper (prctl).
	Subreaper func() error
	// InstallSignal registers the explicit PID-1 signal handlers.
	InstallSignal func()
	// Mount runs the early filesystem mounts.
	Mount func([]mounts.MountSpec) error
	// StartReap launches the orphan reap loop for the process lifetime.
	StartReap func(context.Context)
	// SkipMounts honors --no-early-mounts (container environments).
	SkipMounts bool
}

// RunPhase0 executes the pre-userspace boot obligations in the order
// the kernel requires: the pidfd precondition first (nothing below is
// sound without it), subreaper next (a SIGCHLD needs a reaper), signal
// handlers after that (PID 1 has no implicit defaults), the reap loop
// started, then early mounts before any subsystem assumes them.
//
// Every step that can fail fails closed - a broken /proc corrupts
// everything after it, and a kernel without pidfd makes signal
// delivery unsound for the whole process lifetime.
func RunPhase0(ctx context.Context, d Phase0Deps) error {
	if d.RequirePidfd != nil {
		if err := d.RequirePidfd(); err != nil {
			return err
		}
	}
	if d.Subreaper != nil {
		if err := d.Subreaper(); err != nil {
			return err
		}
	}
	if d.InstallSignal != nil {
		d.InstallSignal()
	}
	if d.StartReap != nil {
		d.StartReap(ctx)
	}
	if !d.SkipMounts && d.Mount != nil {
		if err := d.Mount(mounts.EarlyMounts); err != nil {
			return err
		}
	}
	return nil
}
