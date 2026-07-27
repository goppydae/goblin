package magelib

import (
	"fmt"
	"strings"

	"github.com/magefile/mage/sh"
)

// FuzzAll discovers every Fuzz* target in the repo and runs each for the
// bounded fuzztime (e.g. "10s"). Zero targets is a loud no-op, not an error:
// the target's presence keeps the contract; corpora grow when fuzz targets
// land.
func FuzzAll(fuzztime string) error {
	pkgsOut, err := sh.Output("go", "list", "./...")
	if err != nil {
		return fmt.Errorf("go list: %w", err)
	}

	ran := 0
	for pkg := range strings.SplitSeq(strings.TrimSpace(pkgsOut), "\n") {
		if pkg == "" {
			continue
		}
		listOut, err := sh.Output("go", "test", "-list", "^Fuzz", pkg)
		if err != nil {
			return fmt.Errorf("listing fuzz targets in %s: %w", pkg, err)
		}
		for line := range strings.SplitSeq(strings.TrimSpace(listOut), "\n") {
			name := strings.TrimSpace(line)
			if !strings.HasPrefix(name, "Fuzz") {
				continue
			}
			ran++
			fmt.Printf("Fuzzing %s in %s (-fuzztime %s)...\n", name, pkg, fuzztime)
			if err := sh.RunV("go", "test", "-run", "^$", "-fuzz", "^"+name+"$", "-fuzztime", fuzztime, pkg); err != nil {
				return fmt.Errorf("fuzz %s %s: %w", pkg, name, err)
			}
		}
	}

	if ran == 0 {
		fmt.Println("FUZZ: no Fuzz* targets found in this repo; nothing fuzzed (loud no-op).")
	}
	return nil
}
