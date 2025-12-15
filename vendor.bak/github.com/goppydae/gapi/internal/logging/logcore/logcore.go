package logcore

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

var logger zerolog.Logger

func Init(level zerolog.Level) {
	zerolog.TimeFieldFormat = time.DateTime
	logger = zerolog.New(os.Stdout).With().
		Timestamp().
		Str("stream", "runtime").
		Logger().
		Level(level)
}

func Info() *zerolog.Event  { return logger.Info() }
func Debug() *zerolog.Event { return logger.Debug() }
func Warn() *zerolog.Event  { return logger.Warn() }
func Error() *zerolog.Event { return logger.Error() }
func Fatal() *zerolog.Event { return logger.Fatal() }

func With() zerolog.Context { return logger.With() }
