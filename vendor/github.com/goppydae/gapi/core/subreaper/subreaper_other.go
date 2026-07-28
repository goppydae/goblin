//go:build !linux

package subreaper

import (
	"context"
	"os"
	"syscall"
)

// BecomeSubreaper is unavailable off Linux (prctl).
func BecomeSubreaper() error { return ErrUnsupported }

// ReapLoop is a no-op off Linux; it returns when ctx is done so
// callers keep a uniform shape.
func ReapLoop(ctx context.Context, sigchld <-chan os.Signal, notify func(pid int, ws syscall.WaitStatus)) {
	<-ctx.Done()
}
