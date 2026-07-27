// Package safeio centralizes every variable-path file open in gapi. All
// operator-supplied and discovered paths funnel through here: paths are
// cleaned and made absolute, and the *Under variants refuse to escape
// their root. This is the audited chokepoint for path-traversal (CWE-22)
// concerns; open a file through this package, not os, whenever the path
// is not a literal.
package safeio

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve cleans path and makes it absolute against the process working
// directory. Empty paths are rejected.
func Resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("safeio: empty path")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("safeio: resolve %q: %w", path, err)
	}
	return abs, nil
}

// ResolveUnder resolves path and rejects it unless the result stays at or
// under root. The check is lexical: symlinks inside an operator-owned root
// are the operator's to manage.
func ResolveUnder(root, path string) (string, error) {
	absRoot, err := Resolve(root)
	if err != nil {
		return "", err
	}
	abs, err := Resolve(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("safeio: path %q escapes root %q", path, root)
	}
	return abs, nil
}

// Open opens the resolved path for reading.
func Open(path string) (*os.File, error) {
	p, err := Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// Create creates or truncates the resolved path.
func Create(path string) (*os.File, error) {
	p, err := Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Create(p)
}

// ReadFile reads the resolved path.
func ReadFile(path string) ([]byte, error) {
	p, err := Resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// OpenUnder opens path for reading after confining it to root.
func OpenUnder(root, path string) (*os.File, error) {
	p, err := ResolveUnder(root, path)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// ReadFileUnder reads path after confining it to root.
func ReadFileUnder(root, path string) ([]byte, error) {
	p, err := ResolveUnder(root, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
