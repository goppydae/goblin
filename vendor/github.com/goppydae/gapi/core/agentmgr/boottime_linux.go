//go:build linux

package agentmgr

import (
	"context"
	"log/slog"
	"time"

	"github.com/goppydae/gapi/internal/logattr"
	"golang.org/x/sys/unix"
)

// BootTime returns the instant the system booted.
//
// Derived from the kernel's uptime rather than read from a file, so it
// needs no /proc mount - which matters in PID 1 mode, where the schedule
// may be parsed before the early mount phase has finished.
//
// On failure it falls back to the current time, which degrades OnBootSec
// to OnStartupSec rather than refusing to schedule. That is announced,
// not silent.
func BootTime() time.Time {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn,
			"cannot determine boot time; OnBootSec will be measured from now",
			logattr.Module("agentmgr"), logattr.Err(err))
		return time.Now()
	}
	return time.Now().Add(-time.Duration(si.Uptime) * time.Second)
}
