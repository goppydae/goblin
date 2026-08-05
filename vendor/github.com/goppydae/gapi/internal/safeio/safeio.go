// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package safeio centralizes every variable-path file open in gapi. All
// operator-supplied and discovered paths funnel through here: paths are
// cleaned and made absolute, and the *Under variants refuse to escape
// their root. This is the audited chokepoint for path-traversal (CWE-22)
// concerns; open a file through this package, not os, whenever the path
// is not a literal.
package safeio

import (
	"errors"
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

// ReplaceOwnerOnly atomically replaces path with data, and the result is
// readable only by its owner. Use it for key material and anything else
// where the mode is part of the contract.
//
// The bytes go to a temporary file in the destination's own directory -
// same directory because rename is only atomic within a filesystem -
// which is then renamed over path. os.CreateTemp opens with O_EXCL at
// 0600, and umask can only clear permission bits, never add them, so the
// data is owner-only from the instant it exists.
//
// Replacing rather than writing through is the load-bearing part. A
// create mode applies only when the file does not already exist, and a
// trailing chmod runs after the bytes are on disk; either way an
// overwrite leaves the secret at the old file's mode for the duration of
// the write. A replaced destination has no old mode to inherit.
func ReplaceOwnerOnly(path string, data []byte) error {
	p, err := Resolve(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".tmp*")
	if err != nil {
		return fmt.Errorf("safeio: create temp for %q: %w", path, err)
	}
	name := tmp.Name()
	if err := writeSyncClose(tmp, data); err != nil {
		return errors.Join(fmt.Errorf("safeio: write %q: %w", path, err), remove(name))
	}
	if err := os.Rename(name, p); err != nil {
		return errors.Join(fmt.Errorf("safeio: replace %q: %w", path, err), remove(name))
	}
	return nil
}

// writeSyncClose writes data to f, flushes it to stable storage and
// closes f, reporting the first failure. f is closed on every path.
func writeSyncClose(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// remove deletes a temporary file left behind by a failed replace. An
// already-absent file is not a failure; anything else is reported so the
// caller learns that partial key material may still be on disk.
func remove(name string) error {
	if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("safeio: remove temp %q: %w", name, err)
	}
	return nil
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
