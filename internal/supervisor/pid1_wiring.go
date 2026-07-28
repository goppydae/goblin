package supervisor

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	gapilogging "github.com/goppydae/gapi/core/logging"
	gapimounts "github.com/goppydae/gapi/core/mounts"
	gapipid1 "github.com/goppydae/gapi/core/pid1"
	gapishutdown "github.com/goppydae/gapi/core/shutdown"
	gapisubreaper "github.com/goppydae/gapi/core/subreaper"
	gapisupervisor "github.com/goppydae/gapi/core/supervisor"
	gapiwatchdog "github.com/goppydae/gapi/core/watchdog"
	"github.com/goppydae/goblin/internal/logattr"
)

// pid1Completion tears the machine down after the run context ends, in
// the reversed phase order the design doc requires (the distributed
// layer is drained and left by the caller before this runs; here we
// stop local agents, then sync/umount/reboot). Rootless containers
// cannot reboot - the executor falls through to exit, which IS
// container-init poweroff.
type pid1Completion struct {
	kmsg       *gapilogging.Kmsg
	mountTable []gapimounts.MountSpec
	grace      time.Duration
}

// enablePid1 runs Phase 0 (pre-userspace) before any cluster code, per
// goblin-architecture.md: subreaper, PID-1 signals, kmsg, reaping, and
// early mounts all land before the network stack exists. Shutdown
// signals surface by cancelling runCancel; the returned completion runs
// the local teardown after Run's cluster teardown has unwound.
func (s *Supervisor) enablePid1(ctx context.Context, runCancel context.CancelFunc) (*pid1Completion, error) {
	kmsg := gapilogging.NewKmsg(os.Getenv("GOBLIN_KMSG_PATH"))
	kmsg.Log(gapilogging.KmsgInfo, "phase 0: pre-userspace (goblind)")

	reapKick := make(chan os.Signal, 8)
	gapipid1.Install(ctx, gapipid1.Handlers{
		Shutdown: func(sig os.Signal) {
			kmsg.Log(gapilogging.KmsgInfo, "shutdown signal "+sig.String())
			runCancel()
		},
		Reap: func() {
			select {
			case reapKick <- syscall.SIGCHLD:
			default:
			}
		},
	})

	skipMounts := s.cfg.NoEarlyMounts
	err := gapisupervisor.RunPhase0(ctx, gapisupervisor.Phase0Deps{
		Subreaper: gapisubreaper.BecomeSubreaper,
		Mount: func(specs []gapimounts.MountSpec) error {
			return gapimounts.MountEarly(gapimounts.SysMounter{}, specs)
		},
		StartReap: func(ctx context.Context) {
			go gapisubreaper.ReapLoop(ctx, reapKick, func(pid int, ws syscall.WaitStatus) {
				// The gapi agent manager exists a few lines into Run;
				// an orphan in that window is still reaped, only its
				// agent attribution is skipped.
				if s.agentMgr != nil {
					s.agentMgr.NotifyExited(pid, ws)
				}
			})
		},
		SkipMounts: skipMounts,
	})
	if err != nil {
		kmsg.Log(gapilogging.KmsgErr, "phase 0 failed: "+err.Error())
		return nil, err
	}
	kmsg.Log(gapilogging.KmsgInfo, "phase 0 complete (goblind)")

	if s.cfg.WatchdogDevice != "" {
		interval := s.cfg.WatchdogInterval
		if interval <= 0 {
			interval = 10 * time.Second
		}
		wd, werr := gapiwatchdog.Open(s.cfg.WatchdogDevice, interval)
		if werr != nil {
			return nil, werr
		}
		go wd.Run(ctx)
	}

	grace := s.cfg.ShutdownGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	var mountTable []gapimounts.MountSpec
	if !skipMounts {
		mountTable = gapimounts.EarlyMounts
	}
	return &pid1Completion{kmsg: kmsg, mountTable: mountTable, grace: grace}, nil
}

// complete runs the local teardown and the reboot(2) executor. stopper
// is the gapi agent manager (StopAll); it is passed at call time
// because it is constructed after enablePid1 runs.
func (c *pid1Completion) complete(ctx context.Context, stopper gapishutdown.AgentStopper) {
	c.kmsg.Log(gapilogging.KmsgInfo, "system shutdown: local teardown")
	if err := gapishutdown.SystemShutdown(gapishutdown.SysCalls{}, stopper, c.mountTable, gapishutdown.PowerOff, c.grace); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "reboot unavailable, exiting as container init", logattr.Err(err))
		c.kmsg.Log(gapilogging.KmsgWarn, "reboot unavailable, exiting")
	}
}
