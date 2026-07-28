package lifecycle

import (
	"context"

	"github.com/goppydae/gapi/core/adk/meta"
)

type Agent interface {
	Initialize() error
	Start() error
	Stop() error
	Restart() error
	Reload() error
	Describe() *meta.AgentInfo
	ID() string
	Type() string
	Scope() string
}

type Runner interface {
	// Start spawns the runner's process. The context bounds the start
	// operation only - it is cancelled as soon as Start returns, so an
	// implementation must NOT tie the spawned process's lifetime to it.
	// exec.CommandContext here means the process is SIGKILLed the moment
	// the start call completes (GAPI-DIV-028); use exec.Command and let
	// Stop own the process.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Reload(ctx context.Context) error
	Reset()
}

// Optional capability for runners to support per-start correlation.
type RunIDSetter interface {
	SetRunID(string)
}
