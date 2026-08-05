// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package pid1 installs the explicit PID-1 signal semantics
// (GAPI-DIV-027). The kernel suppresses default signal handling for
// pid 1 - SIGTERM to an init without a handler is silently dropped -
// so every signal this process answers is registered explicitly, with
// no implicit defaults.
package pid1

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Handlers holds the explicit semantics for each PID-1 signal. A nil
// handler absorbs its signal (registered but inert): PID 1 must never
// die to a default disposition.
type Handlers struct {
	// Shutdown answers SIGTERM (graceful shutdown) and SIGINT
	// (ctrl-alt-del via the kernel); the signal is passed through so
	// the executor can distinguish them.
	Shutdown func(os.Signal)
	// Reload answers SIGHUP.
	Reload func()
	// Reap answers SIGCHLD - a kick for the subreaper's reap loop.
	Reap func()
	// Debug answers SIGUSR1 (rotate logs / debug dump).
	Debug func()
	// Emergency answers SIGUSR2 (emergency shell hook).
	Emergency func()
}

// pid1Signals is the doc's explicit signal set.
var pid1Signals = []os.Signal{
	syscall.SIGTERM,
	syscall.SIGINT,
	syscall.SIGHUP,
	syscall.SIGCHLD,
	syscall.SIGUSR1,
	syscall.SIGUSR2,
}

// Install registers the PID-1 signal set and dispatches to the
// handlers until ctx ends or the returned stop function is called.
func Install(ctx context.Context, h Handlers) (stop func()) {
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, pid1Signals...)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case sig := <-ch:
				switch sig {
				case syscall.SIGTERM, syscall.SIGINT:
					if h.Shutdown != nil {
						h.Shutdown(sig)
					}
				case syscall.SIGHUP:
					if h.Reload != nil {
						h.Reload()
					}
				case syscall.SIGCHLD:
					if h.Reap != nil {
						h.Reap()
					}
				case syscall.SIGUSR1:
					if h.Debug != nil {
						h.Debug()
					}
				case syscall.SIGUSR2:
					if h.Emergency != nil {
						h.Emergency()
					}
				}
			}
		}
	}()

	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		signal.Stop(ch)
		close(done)
	}
}
