// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/goppydae/gapi/core/logging"
	"github.com/goppydae/gapi/core/mounts"
	"github.com/goppydae/gapi/core/pid1"
	"github.com/goppydae/gapi/core/procsig"
	"github.com/goppydae/gapi/core/product"
	"github.com/goppydae/gapi/core/shutdown"
	"github.com/goppydae/gapi/core/subreaper"
	"github.com/goppydae/gapi/core/watchdog"
	"github.com/goppydae/gapi/internal/logattr"
)

// defaultShutdownGrace bounds StopAll during a PID-1 teardown when the
// config does not say otherwise.
const defaultShutdownGrace = 10 * time.Second

// EnablePid1 runs Phase 0 (pre-userspace) with the real kernel
// packages and installs the PID-1 signal semantics. Shutdown requests
// (signals here, the system.shutdown topic elsewhere) surface on
// ShutdownRequests(); the returned completion function runs after the
// runtime has stopped: sync, reverse umount, then reboot(2). Where
// reboot is not permitted (containers), the completion returns and
// the caller exits; for a container init, exiting IS poweroff.
func (s *Supervisor) EnablePid1(ctx context.Context) (func(action shutdown.Action), error) {
	kmsg := logging.NewKmsg(os.Getenv(product.EnvKey("KMSG_PATH")))
	kmsg.Log(logging.KmsgInfo, "phase 0: pre-userspace")

	reapKick := make(chan os.Signal, 8)

	// Signals first: PID 1 has no implicit defaults, so the handlers
	// must exist before anything can die or ask us to stop.
	pid1.Install(ctx, pid1.Handlers{
		Shutdown: func(sig os.Signal) {
			kmsg.Log(logging.KmsgInfo, "shutdown signal "+sig.String())
			s.RequestShutdown(shutdown.PowerOff)
		},
		Reap: func() {
			select {
			case reapKick <- syscall.SIGCHLD:
			default:
			}
		},
	})

	skipMounts := s.cfg.Supervisor.NoEarlyMounts
	if err := RunPhase0(ctx, Phase0Deps{
		RequirePidfd: procsig.RequirePidfd,
		Subreaper:    subreaper.BecomeSubreaper,
		Mount: func(specs []mounts.MountSpec) error {
			return mounts.MountEarly(mounts.SysMounter{}, specs)
		},
		StartReap: func(ctx context.Context) {
			go subreaper.ReapLoop(ctx, reapKick, s.manager.NotifyExited)
		},
		SkipMounts: skipMounts,
	}); err != nil {
		kmsg.Log(logging.KmsgErr, "phase 0 failed: "+err.Error())
		return nil, fmt.Errorf("phase 0: %w", err)
	}
	kmsg.Log(logging.KmsgInfo, "phase 0 complete")

	if s.cfg.Supervisor.Watchdog.Enabled {
		dev := s.cfg.Supervisor.Watchdog.Device
		if dev == "" {
			dev = "/dev/watchdog"
		}
		interval := 10 * time.Second
		if v, err := time.ParseDuration(s.cfg.Supervisor.Watchdog.Interval); err == nil && v > 0 {
			interval = v
		}
		wd, err := watchdog.Open(dev, interval)
		if err != nil {
			return nil, fmt.Errorf("watchdog: %w", err)
		}
		go wd.Run(ctx)
	}

	grace := defaultShutdownGrace
	if v, err := time.ParseDuration(s.cfg.Supervisor.Shutdown.GracePeriod); err == nil && v > 0 {
		grace = v
	}
	var mountTable []mounts.MountSpec
	if !skipMounts {
		mountTable = mounts.EarlyMounts
	}

	complete := func(action shutdown.Action) {
		kmsg.Log(logging.KmsgInfo, "system shutdown: teardown")
		if err := shutdown.SystemShutdown(shutdown.SysCalls{}, s.manager, mountTable, action, grace); err != nil {
			// reboot(2) unavailable (rootless container, missing
			// CAP_SYS_BOOT): the process exits and the container init
			// semantics complete the poweroff.
			s.logger.LogAttrs(context.Background(), slog.LevelWarn,
				"reboot unavailable, exiting as container init", logattr.Err(err))
			kmsg.Log(logging.KmsgWarn, "reboot unavailable, exiting")
		}
	}
	return complete, nil
}
