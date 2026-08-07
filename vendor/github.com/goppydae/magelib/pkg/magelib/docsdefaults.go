// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultEntry is one key in a repo's generated defaults.json.
type DefaultEntry struct {
	Value  string `json:"value"`
	Env    string `json:"env"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

// Transcription is one place a document spells a default's value instead
// of rendering it.
type Transcription struct {
	Path  string
	Line  int
	Key   string
	Value string
}

// DocsDefaultsError reports documents that transcribe a configured
// default.
type DocsDefaultsError struct {
	Findings []Transcription
}

func (e *DocsDefaultsError) Error() string {
	var b strings.Builder
	b.WriteString("documentation transcribes configured defaults; render them with the `default` shortcode instead")
	for _, f := range e.Findings {
		fmt.Fprintf(&b, "\n  %s:%d: %q is the value of %s", f.Path, f.Line, f.Value, f.Key)
	}
	return b.String()
}

// shortcodeSpan matches a Hugo shortcode call, whose arguments name a
// KEY and so must not be read as prose spelling a value.
var shortcodeSpan = regexp.MustCompile(`\{\{[<%].*?[>%]\}\}`)

// checkableValue reports whether a default's value is distinctive enough
// to search for in prose, and why not when it is not.
//
// THIS IS A DEVIATION FROM THE DESIGN, and it is load-bearing. The spec
// says to scan for "literal occurrences of any value in defaults.json",
// which is correct for the defect that motivated the gate - the hub
// quoting 127.0.0.1:8080 as a metrics address - and unusable for the
// rest of the schema. Real defaults in this silo include "info", "json",
// 100, 3, 28, true and false. A gate searching prose for the literal
// "100" or "info" reports every page and would be switched off within a
// day, which is worse than not having it: a disabled gate looks like a
// gate.
//
// The rule is therefore stated rather than tuned, and every exclusion is
// PRINTED on each run. A gate that silently narrows its own scope reads
// as full coverage, and this whole exercise exists because a defect
// survived being found once already.
func checkableValue(v string) (ok bool, why string) {
	switch {
	case strings.TrimSpace(v) == "":
		return false, "empty"
	case v == "true" || v == "false":
		return false, "boolean; the literal occurs in ordinary prose"
	default:
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return false, "bare number; the literal occurs in ordinary prose"
		}
		if len(v) < 6 {
			return false, "shorter than 6 characters; too short to distinguish from prose"
		}
	}
	return true, ""
}

// LoadDefaults reads a repo's generated defaults.json.
func LoadDefaults(path string) (map[string]DefaultEntry, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is derived from validated repo-relative config
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out map[string]DefaultEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return out, nil
}

// CheckDocsDefaults fails when a document spells a configured default's
// value instead of rendering it from defaults.json.
//
// waivers name a document that legitimately spells one value, in the
// form "<repo-relative path>:<key>". The pairing is deliberate: a
// blanket file waiver would hide the NEXT transcription in the same
// file, and a configuration example that shows one address has no claim
// on the others. gapi/config/config.yaml is the motivating case - its
// comment explains that "localhost" is not a synonym, because it can
// resolve to ::1 and would bind a different address than the default it
// claims to show. A gate that could not express that exception would be
// wrong.
//
// skips prune paths from the walk entirely, through the same Skip type
// every other gate in this library takes.
func CheckDocsDefaults(cfg DocsConfig, waivers []string, skips ...Skip) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	defaults, err := LoadDefaults(filepath.Join(cfg.Dir, "data", "defaults.json"))
	if err != nil {
		return err
	}
	if len(defaults) == 0 {
		return fmt.Errorf("docs defaults check: defaults.json declares no keys; a gate over no values is green and means nothing")
	}
	skipPaths, skipNames, err := compileSkips("docs defaults check", skips)
	if err != nil {
		return err
	}
	waived, err := compileWaivers(waivers, defaults)
	if err != nil {
		return err
	}

	checked, excluded := partitionDefaults(defaults)
	if len(excluded) > 0 {
		fmt.Printf("Defaults gate is not searching for %d of %d values:\n  %s\n",
			len(excluded), len(defaults), strings.Join(excluded, "\n  "))
	}
	if len(checked) == 0 {
		return fmt.Errorf("docs defaults check: no value in defaults.json is distinctive enough to search for; the gate would pass without checking anything")
	}

	root := filepath.Join(cfg.Dir, "content")
	var findings []Transcription
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipNames[d.Name()] || skipPaths[filepath.Clean(path)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		found, err := scanTranscriptions(path, checked, waived)
		if err != nil {
			return err
		}
		findings = append(findings, found...)
		return nil
	})
	if err != nil {
		return fmt.Errorf("docs defaults check: walking %s: %w", root, err)
	}

	if len(findings) > 0 {
		return &DocsDefaultsError{Findings: findings}
	}
	fmt.Printf("No document transcribes a configured default (%d values checked)\n", len(checked))
	return nil
}

// partitionDefaults splits the keys into those the gate searches for and
// a human-readable account of those it does not.
func partitionDefaults(defaults map[string]DefaultEntry) (map[string]string, []string) {
	checked := map[string]string{}
	var excluded []string
	keys := make([]string, 0, len(defaults))
	for k := range defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ok, why := checkableValue(defaults[k].Value); ok {
			checked[k] = defaults[k].Value
		} else {
			excluded = append(excluded, fmt.Sprintf("%s (%q): %s", k, defaults[k].Value, why))
		}
	}
	return checked, excluded
}

// compileWaivers validates "<path>:<key>" pairs against the defaults
// actually declared.
//
// A waiver naming a key that no longer exists is REJECTED rather than
// ignored. It is debt whose subject is gone, and leaving it in place is
// how a waiver file outlives the thing it excused and starts reading as
// policy.
func compileWaivers(waivers []string, defaults map[string]DefaultEntry) (map[string]bool, error) {
	out := map[string]bool{}
	for _, w := range waivers {
		path, key, ok := strings.Cut(w, ":")
		if !ok || strings.TrimSpace(path) == "" || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("docs defaults check: waiver %q is not in the form \"<path>:<key>\"", w)
		}
		if _, exists := defaults[key]; !exists {
			return nil, fmt.Errorf("docs defaults check: waiver %q names key %q, which defaults.json does not declare; drop the waiver", w, key)
		}
		out[filepath.Clean(path)+":"+key] = true
	}
	return out, nil
}

// scanTranscriptions reports every place path spells one of the checked
// values outside a fenced code block.
//
// Fenced blocks are excluded because a fenced example is showing a
// configuration file, which is the one context where writing the literal
// is the point. Inline code spans are NOT excluded: a value in backticks
// mid-sentence is prose quoting a default, and it is the commonest form
// of exactly the defect this gate exists for.
func scanTranscriptions(path string, checked map[string]string, waived map[string]bool) ([]Transcription, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from a walk rooted at the validated docs dir
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	keys := make([]string, 0, len(checked))
	for k := range checked {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	clean := filepath.Clean(path)
	var found []Transcription
	inFence := false
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		// A shortcode's arguments name a key, so they are not prose
		// spelling a value.
		bare := shortcodeSpan.ReplaceAllString(line, "")
		for _, key := range keys {
			if !strings.Contains(bare, checked[key]) {
				continue
			}
			if waived[clean+":"+key] {
				continue
			}
			found = append(found, Transcription{
				Path: clean, Line: i + 1, Key: key, Value: checked[key],
			})
		}
	}
	return found, nil
}
