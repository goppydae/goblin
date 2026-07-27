package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// Server provides an HTTP endpoint for Prometheus metrics
type Server struct {
	addr   string
	server *http.Server
	logger zerolog.Logger
}

// NewServer creates a new metrics server
func NewServer(addr string, logger zerolog.Logger) *Server {
	mux := http.NewServeMux()
	// Serve GAPI's dedicated registry rather than the global default registry.
	mux.Handle("/metrics", Handler())

	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// The ResponseWriter is the terminus: a failed write means the
		// client is gone; nothing upstream can act on it beyond a log.
		if _, err := fmt.Fprintln(w, "OK"); err != nil {
			logger.Warn().Err(err).Msg("health endpoint write failed")
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
		logger: logger.With().Str("component", "metrics").Logger(),
	}
}

// Start starts the metrics HTTP server
func (s *Server) Start() error {
	s.logger.Info().Str("addr", s.addr).Msg("starting metrics server")

	if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("metrics server failed: %w", err)
	}
	return nil
}

// Stop gracefully stops the metrics server
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info().Msg("stopping metrics server")
	return s.server.Shutdown(ctx)
}
