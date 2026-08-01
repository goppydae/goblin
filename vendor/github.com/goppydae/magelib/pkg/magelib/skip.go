package magelib

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Skip names something a gate must not walk, and why.
//
// Every gate in this library consumes this one type through
// compileSkips, and that is the point: skip parsing used to be written
// per gate, the copies drifted, and the older copy validated nothing.
// Correctness here is not a property each new gate has to rediscover -
// a gate that accepts a Skip inherits it.
//
// A skip that silently matches nothing - or silently matches everything
// - is worse than no skip at all: the gate goes quiet and quiet is
// indistinguishable from clean. That is why the shapes which cannot
// mean what the caller intended are rejected rather than accepted and
// ignored.
type Skip struct {
	// Name is a repo-relative path when it contains "/", otherwise a
	// base name matched at any depth.
	//
	//   - No separator ("adk.go"): matches any file OR directory with
	//     that BASE NAME, at any depth. Broad.
	//   - Contains a separator ("docs/legacy.md"): matches that one
	//     repo-relative path, and nothing else.
	//
	// A match on a directory prunes it; a match on a file skips it.
	Name string
	// Reason states why the rule does not reach it.
	//
	// Required. It does not make a wrong reason impossible, but it makes
	// the claim reviewable: "gopy output, non-standard generated header"
	// is checkable, while a bare path in a slice is not. A skip is a
	// permanent exemption the rule itself grants, and the distinction
	// between that and debt only works if the classification is auditable
	// at the point it is made.
	Reason string
}

// compileSkips validates skips and splits them into the two sets a walk
// needs: exact repo-relative paths, and base names matched at any depth.
//
// gate names the caller ("terminology check", "file length check") so a
// rejection tells the operator which configuration to edit. Every
// rejection is an ERROR rather than a warning: the gate must not run at
// all on a configuration whose skips cannot do what they say.
func compileSkips(gate string, skips []Skip) (paths, names map[string]bool, err error) {
	paths = make(map[string]bool)
	names = make(map[string]bool)
	for _, s := range skips {
		if strings.TrimSpace(s.Reason) == "" {
			return nil, nil, fmt.Errorf(
				"%s: skip %q has no reason; a skip is a permanent exemption and must state why the rule does not reach it",
				gate, s.Name)
		}
		if !strings.Contains(s.Name, "/") {
			// A bare name is not path-validated - it is a base name, not
			// a path - but "", "." and ".." are the walk root's own name
			// and would prune the entire tree and report clean, which is
			// the one failure a gate must never have.
			if s.Name == "" || s.Name == "." || s.Name == ".." {
				return nil, nil, fmt.Errorf(
					"%s: skip %q would prune the whole tree", gate, s.Name)
			}
			names[s.Name] = true
			continue
		}
		if filepath.IsAbs(s.Name) {
			return nil, nil, fmt.Errorf(
				"%s: skip %q must be repo-relative, not absolute", gate, s.Name)
		}
		cleaned := filepath.Clean(s.Name)
		if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
			return nil, nil, fmt.Errorf(
				"%s: skip %q does not name a path inside the repo", gate, s.Name)
		}
		paths[cleaned] = true
	}
	return paths, names, nil
}
