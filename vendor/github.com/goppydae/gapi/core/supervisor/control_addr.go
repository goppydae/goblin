// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// The daemon half of the runtime control address (GAPI-DIV-070): where
// this process actually bound, published so a control binary can find it
// without sharing an environment. The reading half and the tier list are
// in core/config/control_addr.go.

package supervisor

import (
	"context"
	"log/slog"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/core/eventbus"
	"github.com/goppydae/gapi/internal/logattr"
)

// publishControlAddr writes where the daemon ACTUALLY BOUND to the
// runtime address file, so a control binary can find it without sharing
// an environment (GAPI-DIV-070).
//
// It asks the TRANSPORT rather than reading cfg.Transport.Address,
// because those are different values whenever it matters: ":0" resolves
// to a kernel-assigned port and a hostname may resolve to something
// other than what was written. Publishing the configured value would
// reintroduce the defect one layer up. A local transport has no address
// and reports "", so nothing is published.
//
// NOT FATAL, deliberately. A read-only /run in a container would
// otherwise stop a daemon that is healthy and perfectly reachable with
// an explicit address. It is WARN rather than Info because the
// consequence is real: every control call that does not carry an
// address will fail to find this daemon.
func publishControlAddr(logger *slog.Logger, t eventbus.Transport[*anypb.Any]) {
	reporter, ok := t.(interface{ Addr() string })
	if !ok {
		return
	}
	bound := reporter.Addr()
	if bound == "" {
		return
	}
	path, err := config.WriteControlAddr(bound)
	if err != nil {
		logger.LogAttrs(context.Background(), slog.LevelWarn,
			"could not publish the control address; clients must be given one explicitly",
			logattr.Addr(bound), logattr.Err(err))
		return
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo,
		"published control address", logattr.Addr(bound), logattr.Path(path))
}

// unpublishControlAddr removes the published address during teardown.
//
// It runs FIRST in the shutdown sequence on purpose. A file naming a
// dead port is worse than no file at all: a client reads it, dials
// nothing, and gets the bare timeout that GAPI-DIV-070 exists to
// eliminate. Best effort - a kill -9 skips this entirely, which is why
// the reading side also reports WHERE an address came from.
func unpublishControlAddr(logger *slog.Logger) {
	if err := config.RemoveControlAddr(); err != nil {
		logger.LogAttrs(context.Background(), slog.LevelWarn,
			"could not remove the published control address; it now names a dead port",
			logattr.Err(err))
	}
}
