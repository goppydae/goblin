//go:build linux

package subreaper

import (
	"context"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// BecomeSubreaper marks this process as a child subreaper: orphaned
// descendants reparent to it rather than to pid 1. Called before any
// other initialization (Phase 0); when the process IS pid 1 the call
// is a harmless no-op for correctness but kept for uniformity.
func BecomeSubreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// ReapLoop drains terminated children for the supervisor's lifetime.
// It triggers on SIGCHLD (caller supplies the subscribed channel), on
// a safety tick (coalesced or pre-subscription signal edges must not
// strand zombies), and once at startup. Every reaped pid is forwarded
// to notify with its true wait status; the agent manager decides
// whether it was a known agent or an adopted orphan.
func ReapLoop(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus)) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	drain := func() {
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if err != nil || pid <= 0 {
				// ECHILD (no children) or no zombies right now.
				return
			}
			notify(pid, ws)
		}
	}

	drain()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigchld:
			drain()
		case <-ticker.C:
			drain()
		}
	}
}
