// Package subreaper implements the PID-1 orphan-reaping obligation
// (GAPI-DIV-027): the supervisor registers as a child subreaper so
// orphaned descendants reparent to it instead of pid 1, and a reap
// loop collects their exit statuses - zombie accumulation is a kernel
// obligation, not an optimization. Linux-only by nature; non-Linux
// builds compile and refuse.
package subreaper

import "errors"

// ErrUnsupported: subreaper semantics are a Linux prctl feature.
var ErrUnsupported = errors.New("subreaper: linux-only")
