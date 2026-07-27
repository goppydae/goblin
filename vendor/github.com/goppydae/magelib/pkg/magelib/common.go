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

// Fmt formats all hand-written Go code with goimports (grouping and pruning
// imports as well as formatting; target contract per the go manifesto).
func Fmt() error {
	fmt.Println("Formatting code (goimports)...")
	files, err := goSourceFiles(".")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("No Go source files found")
		return nil
	}
	args := append([]string{"-w"}, files...)
	return sh.RunV("goimports", args...)
}

// Tidy runs go mod tidy
func Tidy() error {
	fmt.Println("Tidying go.mod...")
	return sh.Run("go", "mod", "tidy")
}
