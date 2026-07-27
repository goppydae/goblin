package magelib

import (
	"fmt"
	"os/exec"
	"strings"
)

// CheckShellUnification compares the tool inventory of sibling dev shells by
// resolving each tool's real store path inside every shell. Identical shells
// resolve identical paths; any skew is returned as an error, turning silent
// drift into a red check (ecosystem manifesto section 11, interim measure --
// the shared-nix-module refactor is the recorded target).
//
// shells maps a display name to a flake ref (e.g. "gapi" -> "../gapi").
// The check shells out to `nix develop <ref>`, so it is slower than the
// doctor and lives behind its own target.
func CheckShellUnification(shells map[string]string, tools []string) error {
	type inventory struct {
		name  string
		paths map[string]string
	}
	var inventories []inventory

	for name, ref := range shells {
		script := "for t in " + strings.Join(tools, " ") + "; do printf '%s=' \"$t\"; readlink -f \"$(command -v \"$t\")\" || echo MISSING; done"
		out, err := exec.Command("nix", "develop", ref, "--command", "sh", "-c", script).Output()
		if err != nil {
			return fmt.Errorf("entering dev shell %s (%s): %w", name, ref, err)
		}
		paths := map[string]string{}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok {
				paths[k] = v
			}
		}
		inventories = append(inventories, inventory{name: name, paths: paths})
	}

	if len(inventories) < 2 {
		return fmt.Errorf("shell unification check needs at least two shells")
	}

	var skew []string
	ref := inventories[0]
	for _, other := range inventories[1:] {
		for _, tool := range tools {
			a, b := ref.paths[tool], other.paths[tool]
			if a != b {
				skew = append(skew, fmt.Sprintf("%s: %s=%s vs %s=%s", tool, ref.name, a, other.name, b))
			}
		}
	}
	if len(skew) > 0 {
		return fmt.Errorf("dev shell tool inventories diverge (converge the flakes and locks):\n  %s", strings.Join(skew, "\n  "))
	}
	fmt.Printf("Shell inventories agree across %d shells for %d tools\n", len(inventories), len(tools))
	return nil
}
