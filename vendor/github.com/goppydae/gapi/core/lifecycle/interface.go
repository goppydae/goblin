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
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Reload(ctx context.Context) error
	Reset()
}

// Optional capability for runners to support per-start correlation.
type RunIDSetter interface {
	SetRunID(string)
}
