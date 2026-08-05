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
	"log/slog"
	"syscall"

	"github.com/goppydae/gapi/internal/logattr"
)

// NotifyExited receives a reaped child's true exit status from the
// subreaper loop (PID-1 mode). Known agent processes are logged with
// their identity - their own exit watchers drive the state change;
// the wait status recorded here is the authoritative one, since the
// reaper may win the wait race. Unknown pids are adopted orphans:
// reaped and noted quietly (aggressive logging floods kmsg).
func (am *AgentManager) NotifyExited(pid int, ws syscall.WaitStatus) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for id, a := range am.agents {
		p, ok := a.(interface{ Pid() (int, bool) })
		if !ok {
			continue
		}
		if apid, running := p.Pid(); running && apid == pid {
			slog.Default().LogAttrs(context.Background(), slog.LevelInfo, "agent process reaped",
				logattr.AgentID(id), slog.Int("pid", pid), slog.Int("wait_status", int(ws)))
			return
		}
	}
	slog.Default().LogAttrs(context.Background(), slog.LevelDebug, "adopted orphan reaped",
		slog.Int("pid", pid), slog.Int("wait_status", int(ws)))
}
