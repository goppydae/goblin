package magelib

import (
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/magefile/mage/sh"
)

// skipDirs are directories excluded from source-file walks: build output,
// vendored dependencies, and VCS state are not hand-written project code.
var skipDirs = map[string]bool{
	".git":   true,
	".bin":   true,
	"bin":    true,
	"vendor": true,
}

// goSourceFiles returns every hand-written .go file under root, skipping
// vendor, build output, and VCS directories.
func goSourceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// Fmt formats all hand-written Go code with gofmt.
//
// gofmt and not goimports, which is MAGELIB-DIV-007's resolution
// (operator decision 22) and worth stating because the previous
// spelling looked strictly better.
//
// Lint's format check reads with gofmt. Fmt used to WRITE with
// goimports, and the two tools do not agree: goimports splits stdlib
// from third-party imports while gofmt only sorts within an existing
// group. protoc-gen-go interleaves them, so generated .pb.go is
// gofmt-clean by construction and goimports-dirty - 9 of 9 measured in
// gapi. The result was a stable, silent loop. `mage lint` would fail
// on one hand-written file and name `mage fmt` as the remedy; running
// it rewrote every generated file in the repo; `mage proto` reverted
// them on the next regeneration. Nothing was ever wrong and the diff
// never went away.
//
// Making the WRITER match the CHECKER fixes it with no exclusion list
// at all, because generated output is already gofmt-clean. The
// rejected alternative - check with goimports and exempt generated
// code from both - needs a generated-file predicate, a per-consumer
// skip for gopy output (whose header is non-standard, GAPI-DIV-044),
// and a pinned -local prefix, and it amends the go manifesto, which
// says gofmt is non-negotiable. It is recorded for future
// consideration because it is the only direction that makes import
// GROUPING a merge gate, which this one gives up.
//
// What is given up: goimports also adds and prunes imports. gopls does
// that on save for anyone who wants it, and the one place the build
// genuinely depends on it - the generate-then-goimports ordering that
// fixes gopy's import paths - is its own pinned step in the Python
// binding pipeline, not this target.
func Fmt() error {
	fmt.Println("Formatting code (gofmt)...")
	files, err := goSourceFiles(".")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("No Go source files found")
		return nil
	}
	args := append([]string{"-w"}, files...)
	return sh.RunV("gofmt", args...)
}

// Tidy runs go mod tidy
func Tidy() error {
	fmt.Println("Tidying go.mod...")
	return sh.Run("go", "mod", "tidy")
}
