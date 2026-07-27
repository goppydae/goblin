//go:build tools

// Package tools anchors the pinned gopy dependency so `go mod tidy` tracks
// the full dependency graph of the gopy command.
package tools

import _ "github.com/go-python/gopy"
