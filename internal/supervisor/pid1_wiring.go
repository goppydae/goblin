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
	gapiprocsig "github.com/goppydae/gapi/core/procsig"
	gapiproduct "github.com/goppydae/gapi/core/product"
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
	// Was the literal "GOBLIN_KMSG_PATH" - correctly named, and still a
	// second spelling of a name the kernel composes. Composing it here
	// makes agreement structural (GOBLIN-DIV-055).
	kmsg := gapilogging.NewKmsg(os.Getenv(gapiproduct.EnvKey("KMSG_PATH")))
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
		// goblind supervises agents through the same gapi agent manager
		// gapid does, so it inherits the same dependency on os/exec
		// binding a pidfd at fork. The field is nil-skippable, so
		// leaving it out is silent: Phase 0 would simply never check,
		// and signal delivery would degrade to kill-by-PID on a kernel
		// without pidfd exactly as it did before GAPI-DIV-016.
		RequirePidfd: gapiprocsig.RequirePidfd,
		Subreaper:    gapisubreaper.BecomeSubreaper,
		Mount: func(specs []gapimounts.MountSpec) error {
			return gapimounts.MountEarly(gapimounts.SysMounter{}, specs)
		},
		StartReap: func(ctx context.Context) {
			// tierPreUserspace, not tierRun: this loop must still be
			// reaping while the local teardown's StopAll kills children.
			// Joining it with the cluster loops would break the teardown
			// it exists to serve (GOBLIN-DIV-038).
			s.loops.spawn(tierPreUserspace, "reaper", func() {
				gapisubreaper.ReapLoop(ctx, reapKick, func(pid int, ws syscall.WaitStatus) {
					// The gapi agent manager exists a few lines into Run;
					// an orphan in that window is still reaped, only its
					// agent attribution is skipped.
					if s.agentMgr != nil {
						s.agentMgr.NotifyExited(pid, ws)
					}
				})
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
		// tierPreUserspace: the watchdog has to keep petting until
		// reboot(2) actually fires, or the machine is reset out from
		// under its own shutdown.
		s.loops.spawn(tierPreUserspace, "watchdog", func() { wd.Run(ctx) })
	}

	// One reading of the grace, shared with the loop joins, so the two
	// halves of teardown cannot drift apart.
	grace := s.shutdownGrace()
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
