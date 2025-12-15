package logevent

import (
	"github.com/rs/zerolog"
)

type Event struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`             // e.g., "lifecycle", "eventbus", "status"
	Source  string      `json:"source,omitempty"` // e.g., "goblin.scheduler"
	Payload interface{} `json:"payload,omitempty"`
}

// Log writes a structured event to the given logger.
func Log(logger zerolog.Logger, evt Event) {
	e := logger.Info().Str("event", evt.Type)

	if evt.Source != "" {
		e = e.Str("source", evt.Source)
	}
	if evt.Payload != nil {
		e = e.Interface("data", evt.Payload)
	}

	e.Msg("structured event")
}
