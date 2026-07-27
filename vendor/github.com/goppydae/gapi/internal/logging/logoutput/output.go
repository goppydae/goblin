package logoutput

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/goppydae/gapi/core/config"
)

// SetupOutputs configures multi-output logging based on configuration
func SetupOutputs(cfg *config.LoggingConfig) (io.Writer, error) {
	var writers []io.Writer

	// Always include stdout
	writers = append(writers, os.Stdout)

	// Optional file output
	if cfg.File.Enabled {
		fileWriter, err := setupFileOutput(&cfg.File)
		if err != nil {
			return nil, fmt.Errorf("failed to setup file output: %w", err)
		}
		writers = append(writers, fileWriter)
	}

	// Optional Loki output
	if cfg.Loki.Enabled {
		lokiWriter, err := setupLokiOutput(&cfg.Loki)
		if err != nil {
			return nil, fmt.Errorf("failed to setup loki output: %w", err)
		}
		writers = append(writers, lokiWriter)
	}

	// Return multi-writer
	if len(writers) == 1 {
		return writers[0], nil
	}
	return zerolog.MultiLevelWriter(writers...), nil
}

// setupFileOutput creates a file writer with rotation
func setupFileOutput(cfg *config.FileOutputConfig) (io.Writer, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(cfg.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Use lumberjack for log rotation
	return &lumberjack.Logger{
		Filename:   cfg.Path,
		MaxSize:    cfg.MaxSize, // megabytes
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAge, // days
		Compress:   cfg.Compress,
	}, nil
}

// setupLokiOutput creates a Loki writer.
//
// Loki integration is not yet implemented. Rather than silently falling back
// to stderr (which leaves operators believing logs are being forwarded to
// Loki when they are not), this returns an explicit error so the
// misconfiguration surfaces at startup.
func setupLokiOutput(_ *config.LokiOutputConfig) (io.Writer, error) {
	return nil, fmt.Errorf("loki output is enabled but not implemented; disable logging.loki.enabled or remove the loki configuration")
}

// ParseLevel converts string level to zerolog.Level
func ParseLevel(level string) zerolog.Level {
	switch level {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info":
		return zerolog.InfoLevel
	case "warn":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}
