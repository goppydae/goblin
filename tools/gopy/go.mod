// Tools module: builds the pinned gopy binding-codegen tool.
// gopy v0.4.10 pins golang.org/x/tools v0.16.0, which does not compile under
// go 1.26; the replace lifts x/tools to a compatible version while keeping
// gopy itself at the pinned release. The shell hook builds gopy from here
// into GOBIN, so the pin lives in-repo instead of floating (@latest is
// forbidden; see the divergence ledger).
//
// Goblin has no gopy consumer today; this exists only to keep the dev shells
// symmetric. Dropping gopy from goblin entirely is a recorded operator
// decision (see the convergence report).
module github.com/goppydae/goblin/tools/gopy

go 1.26.0

require github.com/go-python/gopy v0.4.10

require (
	github.com/gonuts/commander v0.1.0 // indirect
	github.com/gonuts/flag v0.1.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/mod v0.30.0 // indirect
	golang.org/x/sync v0.18.0 // indirect
	golang.org/x/tools v0.38.0 // indirect
)

replace golang.org/x/tools => golang.org/x/tools v0.39.0
