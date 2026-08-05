// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package logging is the one place log handlers are built. It turns the
// checked-in LoggingConfig into a *slog.Logger (JSON for machines, text
// for consoles, optional rotating file sink) and is exported so consumers
// embedding the kernel (goblin) reuse the same wiring instead of growing
// their own. Call sites log through slog with typed attribute
// constructors (see internal/logattr); this package only builds handlers.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/goppydae/gapi/core/config"
)

// Build constructs the process logger from configuration: a JSON handler
// by default, a text handler for `format: console`, writing to stdout
// plus an optional rotating file sink. The returned Closer shuts the
// file sink; callers close it on shutdown.
func Build(cfg *config.LoggingConfig) (*slog.Logger, io.Closer, error) {
	// Loki: fail loudly rather than silently dropping forwarding - an
	// operator who enabled it must learn at startup that it is absent.
	if cfg.Loki.Enabled {
		return nil, nil, fmt.Errorf("loki output is enabled but not implemented; disable logging.loki.enabled or remove the loki configuration")
	}

	writers := []io.Writer{os.Stdout}
	var closer io.Closer = nopCloser{}
	if cfg.File.Enabled {
		fw, err := fileWriter(&cfg.File)
		if err != nil {
			return nil, nil, err
		}
		writers = append(writers, fw)
		closer = fw
	}

	out := writers[0]
	if len(writers) > 1 {
		// slog handlers serialize each record into a single Write call,
		// so io.MultiWriter needs no additional locking here.
		out = io.MultiWriter(writers...)
	}

	opts := &slog.HandlerOptions{Level: ParseLevel(cfg.Level)}
	var handler slog.Handler
	if cfg.Format == "console" {
		handler = slog.NewTextHandler(out, opts)
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}
	return slog.New(handler), closer, nil
}

// ParseLevel maps the config-file level names onto slog levels. "trace"
// (a zerolog-era name kept for config compatibility) maps to debug;
// unknown names default to info.
func ParseLevel(level string) slog.Level {
	switch level {
	case "trace", "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func fileWriter(cfg *config.FileOutputConfig) (*lumberjack.Logger, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0750); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	return &lumberjack.Logger{
		Filename:   cfg.Path,
		MaxSize:    cfg.MaxSize, // megabytes
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge, // days
		Compress:   cfg.Compress,
	}, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
