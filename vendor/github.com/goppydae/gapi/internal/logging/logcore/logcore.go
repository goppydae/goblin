package logcore

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/goppydae/gapi/core/config"
	"github.com/goppydae/gapi/internal/logging/logoutput"
)

var logger zerolog.Logger

// Init initializes the logger with a specific level (legacy)
func Init(level zerolog.Level) {
	zerolog.TimeFieldFormat = time.DateTime
	logger = zerolog.New(os.Stdout).With().
		Timestamp().
		Str("stream", "runtime").
		Logger().
		Level(level)
}

// InitWithConfig initializes the logger with full configuration support
func InitWithConfig(cfg *config.LoggingConfig) error {
	zerolog.TimeFieldFormat = time.DateTime

	// Setup multi-output
	output, err := logoutput.SetupOutputs(cfg)
	if err != nil {
		return err
	}

	// Parse log level
	level := logoutput.ParseLevel(cfg.Level)

	// Setup format
	var writer io.Writer
	if cfg.Format == "console" {
		writer = zerolog.ConsoleWriter{Out: output, TimeFormat: time.DateTime}
	} else {
		writer = output
	}

	// Create logger
	logger = zerolog.New(writer).With().
		Timestamp().
		Str("stream", "runtime").
		Logger().
		Level(level)

	return nil
}

func Info() *zerolog.Event  { return logger.Info() }
func Debug() *zerolog.Event { return logger.Debug() }
func Warn() *zerolog.Event  { return logger.Warn() }
func Error() *zerolog.Event { return logger.Error() }
func Fatal() *zerolog.Event { return logger.Fatal() }
func Trace() *zerolog.Event { return logger.Trace() }

func With() zerolog.Context   { return logger.With() }
func GetLevel() zerolog.Level { return logger.GetLevel() }
