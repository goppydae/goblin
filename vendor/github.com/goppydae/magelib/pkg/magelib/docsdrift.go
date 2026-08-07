// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DocsDriftError reports generated documentation that no longer matches
// its source.
//
// The stale paths travel on the error rather than only in its message,
// because a caller that wants to write them out, or a test that wants to
// assert which one moved, should not have to parse prose. Errors are
// data here.
type DocsDriftError struct {
	// Stale are the repo-relative paths whose committed bytes differ
	// from freshly generated ones.
	Stale []string
	// Missing are paths declared Committed that generation did not
	// produce at all.
	Missing []string
	// Untracked are paths generation produced that are not declared
	// Committed, so nothing gates them.
	Untracked []string
}

func (e *DocsDriftError) Error() string {
	var b strings.Builder
	b.WriteString("generated documentation is out of date; run `mage docs:generate` and commit the result")
	if len(e.Stale) > 0 {
		fmt.Fprintf(&b, "\n  stale:\n    %s", strings.Join(e.Stale, "\n    "))
	}
	if len(e.Missing) > 0 {
		fmt.Fprintf(&b, "\n  declared committed but not generated:\n    %s", strings.Join(e.Missing, "\n    "))
	}
	if len(e.Untracked) > 0 {
		fmt.Fprintf(&b, "\n  generated but not declared committed, so nothing gates them:\n    %s", strings.Join(e.Untracked, "\n    "))
	}
	return b.String()
}

// CheckDocsDrift regenerates the documentation into a temporary tree and
// byte-compares it against what is committed.
//
// It never writes to the working tree. A gate that regenerates in place
// REPAIRS the drift it is measuring, so the first run fails, the second
// passes, and the defect is gone before anyone reads the message. That
// is why DocsConfig.Generators take an output root.
//
// The check reports three distinct conditions rather than one. Stale
// means the source moved and the artifact did not. Missing means
// something is declared under drift control that generation does not
// produce, which is a configuration error pointing at a renamed output.
// Untracked means generation produces a file nothing gates - the quiet
// one, and the reason the whole exercise is worth doing: an artifact
// outside Committed can drift forever and this check would stay green.
func CheckDocsDrift(cfg DocsConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if len(cfg.Committed) == 0 {
		return fmt.Errorf("docs drift check: Committed is empty; a drift gate over no paths is green and means nothing")
	}

	tmp, err := os.MkdirTemp("", "magelib-docs-drift-")
	if err != nil {
		return fmt.Errorf("docs drift check: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := docsGenerateInto(cfg, tmp); err != nil {
		return fmt.Errorf("docs drift check: regenerating: %w", err)
	}

	produced, err := generatedPaths(tmp)
	if err != nil {
		return fmt.Errorf("docs drift check: %w", err)
	}

	var drift DocsDriftError
	committed := map[string]bool{}
	for _, rel := range cfg.Committed {
		committed[filepath.Clean(rel)] = true
	}

	for _, rel := range sortedStrings(committed) {
		fresh, freshErr := os.ReadFile(filepath.Join(tmp, rel)) // #nosec G304 -- rel is validated repo-relative config
		if freshErr != nil {
			drift.Missing = append(drift.Missing, rel)
			continue
		}
		current, curErr := os.ReadFile(rel) // #nosec G304 -- rel is validated repo-relative config
		if curErr != nil || !bytes.Equal(fresh, current) {
			drift.Stale = append(drift.Stale, rel)
		}
	}
	for _, rel := range produced {
		if !committed[rel] {
			drift.Untracked = append(drift.Untracked, rel)
		}
	}

	if len(drift.Stale) > 0 || len(drift.Missing) > 0 || len(drift.Untracked) > 0 {
		return &drift
	}
	fmt.Printf("Generated documentation is current across %d paths\n", len(cfg.Committed))
	return nil
}

// generatedPaths lists every file produced under root, as repo-relative
// paths.
func generatedPaths(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// sortedStrings returns a set's members in a stable order, so a failure
// lists the same paths in the same sequence on every run.
func sortedStrings(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
