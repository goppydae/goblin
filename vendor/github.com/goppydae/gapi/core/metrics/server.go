// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package metrics

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/goppydae/gapi/internal/logattr"
)

// Server provides an HTTP endpoint for Prometheus metrics
type Server struct {
	addr   string
	server *http.Server
	logger *slog.Logger
}

// NewServer creates a new metrics server
func NewServer(addr string, logger *slog.Logger) *Server {
	mux := http.NewServeMux()
	// Serve GAPI's dedicated registry rather than the global default registry.
	mux.Handle("/metrics", Handler())

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// The ResponseWriter is the terminus: a failed write means the
		// client is gone; nothing upstream can act on it beyond a log.
		if _, err := fmt.Fprintln(w, "OK"); err != nil {
			logger.LogAttrs(context.Background(), slog.LevelWarn, "health endpoint write failed", logattr.Err(err))
		}
	})

	return &Server{
		addr: addr,
		server: &http.Server{
			Addr:         addr,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
		logger: logger.With(logattr.Component("metrics")),
	}
}

// Start starts the metrics HTTP server
func (s *Server) Start() error {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "starting metrics server", logattr.Addr(s.addr))

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("metrics server failed: %w", err)
	}
	return nil
}

// Stop gracefully stops the metrics server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.LogAttrs(context.Background(), slog.LevelInfo, "stopping metrics server")
	return s.server.Shutdown(ctx)
}
