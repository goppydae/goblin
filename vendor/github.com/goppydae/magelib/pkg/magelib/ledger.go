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
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Structural gates for the divergence and deprecation ledgers.
//
// THE LEDGERS ARE THIS ECOSYSTEM'S HONESTY LAYER AND NOTHING CHECKED
// THEIR SHAPE. `jq -e .` proves each line is JSON and says nothing about
// which keys it carries, so a missing field is invisible until someone
// renders that entry and notices a null - which is how two entries came
// to be filed without a date, out of two hundred and two.
//
// DIVERGENCE AND DEPRECATION ARE NOT THE SAME SHAPE and do not share a
// schema. A divergence is found, then CLOSED ON A GATE: its load-bearing
// field is `exit`, a criterion. A deprecation is announced, then REMOVED
// AT A TAG: its load-bearing field is `target_removal_tag`, a deadline.
// Forcing one schema on both is what produced a `migration_status` that
// reads "complete - zero callers remain" while the symbol is still there.

// Reserved values, and the rule that keeps them honest.
//
// A reserved value is legal ONLY where the schema below permits it, per
// key. Without that restriction "NA" becomes an escape hatch that turns
// any red gate green, which is the same failure as widening a key set to
// make a check pass.
//
//   - null    NOT APPLICABLE. The question does not arise for this
//     entry, and which entries those are is STRUCTURAL rather than a
//     judgement: an open divergence has no closing time, so the gate
//     derives the requirement from `status` instead of trusting the
//     author.
//   - "NA"    APPLIES, DELIBERATELY NOT TRACKED. A closed question.
//   - "NaN"   APPLIES, WAS SOUGHT, COULD NOT BE DETERMINED. An open
//     question, and the difference from "NA" is intent versus attempt.
//
// Both string sentinels are quoted deliberately: a bare NaN is NOT valid
// JSON and both encoding/json and jq reject it.
//
// KNOWN HAZARD, stated because it cannot be gated: a string sentinel
// SORTS AS DATA. `jq 'select(.closed < "2026-09")'` compares "NA"
// byte-wise and silently includes it. Filter reserved values before any
// range query; that is why they are permitted on as few keys as possible.
const (
	reservedNotTracked   = "NA"
	reservedUndetermined = "NaN"
)

// fieldKind is how a value is validated once it is known to be present.
type fieldKind int

const (
	kindProse    fieldKind = iota // non-empty string
	kindEnum                      // non-empty string from a closed set
	kindDateTime                  // RFC3339 with an explicit offset
	kindFiles                     // [{path, symbol?, lines?}], may be empty
	kindRefs                      // [{ref, rel}], may be empty
)

// fieldSpec declares one key.
type fieldSpec struct {
	Name string
	Kind fieldKind
	Enum []string

	// OnlyWhenResolved marks a key whose value is required exactly when
	// the entry is in its terminal state, and which must be null
	// otherwise. The gate derives both directions.
	OnlyWhenResolved bool

	// AllowNull permits the not-applicable value on a key whose absence
	// is not derivable from status - `amended` on an entry never
	// amended, or a deprecation removed with no replacement.
	AllowNull bool

	// AllowNA and AllowNaN permit the string sentinels on this key.
	AllowNA  bool
	AllowNaN bool
}

// ledgerSchema is one ledger's complete key set. Any key outside it is an
// error: a misspelling otherwise produces an entry that parses, passes a
// presence check, and loses a field in silence.
type ledgerSchema struct {
	Kind        string // "divergence" / "deprecation", for messages
	IDPattern   *regexp.Regexp
	TerminalKey string // the status value that means "done"
	Fields      []fieldSpec
}

var (
	divergenceID  = regexp.MustCompile(`^(GAPI|GOBLIN|MAGELIB)-DIV-[0-9]{3}$`)
	deprecationID = regexp.MustCompile(`^(GAPI|GOBLIN|MAGELIB)-DEP-[0-9]{3}$`)

	// owner is the product ROLE, spelled as the running binary logs it.
	ledgerOwners = []string{"build", "orchestrator", "supervisor"}

	// refScheme names the KIND of thing pointed at. Closed, and derived
	// from what entries already cite in prose rather than invented:
	// other ledger entries dominate at 74% of entries, then normative
	// documents, then operator decisions.
	refScheme = regexp.MustCompile(`^(div|dep|doc|decision|note):\S`)

	// refRels is closed. `related` is the honest catch-all: three
	// quarters of the references already written in prose carry no
	// detectable relation, and a wrong specific relation is worse than
	// an accurate vague one.
	refRels = []string{"governs", "blocked-by", "spawned", "supersedes", "same-shape", "related"}
)

// divergenceSchema: found, then closed on a gate.
//
// `exit` and `closure` are separate because one field cannot be both. The
// exit is the criterion the entry was FILED with and does not change;
// the closure is what actually happened. Measured before the split:
// GAPI-DIV-043's exit was rewritten between filing and resolution, so
// the criterion an entry closed against was recoverable only from git -
// and nothing distinguished refining a criterion on evidence from
// lowering the bar to fit what got done.
var divergenceSchema = ledgerSchema{
	Kind:        "divergence",
	IDPattern:   divergenceID,
	TerminalKey: "resolved",
	Fields: []fieldSpec{
		{Name: "id", Kind: kindProse},
		{Name: "scope", Kind: kindProse},
		{Name: "files", Kind: kindFiles},
		{Name: "violation", Kind: kindProse},
		{Name: "rule", Kind: kindProse},
		{Name: "owner", Kind: kindEnum, Enum: ledgerOwners},
		{Name: "status", Kind: kindEnum, Enum: []string{"open", "resolved"}},
		{Name: "exit", Kind: kindProse},
		{Name: "closure", Kind: kindProse, OnlyWhenResolved: true},
		{Name: "opened", Kind: kindDateTime, AllowNaN: true},
		{Name: "closed", Kind: kindDateTime, OnlyWhenResolved: true},
		{Name: "amended", Kind: kindDateTime, AllowNull: true},
		{Name: "refs", Kind: kindRefs},
	},
}

// deprecationSchema: announced, then removed at a tag.
//
// `complete` and `removed` are deliberately distinct states. The one
// pre-existing entry recorded "complete - zero callers remain" while the
// symbol was still present, which is two facts the old shape could only
// express as one.
var deprecationSchema = ledgerSchema{
	Kind:        "deprecation",
	IDPattern:   deprecationID,
	TerminalKey: "removed",
	Fields: []fieldSpec{
		{Name: "id", Kind: kindProse},
		{Name: "scope", Kind: kindProse},
		{Name: "files", Kind: kindFiles},
		{Name: "rationale", Kind: kindProse},
		{Name: "owner", Kind: kindEnum, Enum: ledgerOwners},
		{Name: "status", Kind: kindEnum,
			Enum: []string{"announced", "migrating", "complete", "removed"}},
		{Name: "replacement", Kind: kindProse, AllowNull: true},
		{Name: "target_removal_tag", Kind: kindProse, AllowNA: true},
		{Name: "announced", Kind: kindDateTime, AllowNaN: true},
		{Name: "removed", Kind: kindDateTime, OnlyWhenResolved: true},
		{Name: "refs", Kind: kindRefs},
	},
}

// CheckDivergence validates a divergence ledger's structure.
func CheckDivergence(path string) error { return checkLedger(path, divergenceSchema) }

// CheckDeprecation validates a deprecation ledger's structure.
func CheckDeprecation(path string) error { return checkLedger(path, deprecationSchema) }

func checkLedger(path string, schema ledgerSchema) error {
	raw, err := os.ReadFile(path) //nolint:gosec // caller-declared repo path
	if err != nil {
		return fmt.Errorf("reading %s ledger %s: %w", schema.Kind, path, err)
	}

	known := map[string]fieldSpec{}
	for _, f := range schema.Fields {
		known[f.Name] = f
	}

	var problems []string
	seen := map[string]int{}
	count := 0

	for n, line := range strings.Split(string(raw), "\n") {
		lineNo := n + 1
		if strings.TrimSpace(line) == "" {
			continue
		}
		count++

		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			problems = append(problems,
				fmt.Sprintf("%s:%d: not a JSON object: %v", path, lineNo, err))
			continue
		}

		id, _ := entry["id"].(string)
		where := fmt.Sprintf("%s:%d", path, lineNo)
		if id != "" {
			where = fmt.Sprintf("%s:%d (%s)", path, lineNo, id)
		}

		for k := range entry {
			if _, ok := known[k]; !ok {
				problems = append(problems,
					where+": unknown key "+k+" (a typo loses a field silently)")
			}
		}

		terminal, _ := entry["status"].(string)
		isTerminal := terminal == schema.TerminalKey

		for _, f := range schema.Fields {
			problems = append(problems, checkField(where, entry, f, isTerminal, schema)...)
		}

		// An entry cannot close before it opens. The schema cannot
		// express this per-key, and it is not hypothetical: the
		// migration produced exactly one such row, where a hand-recorded
		// day was later than the commit that closed the entry.
		problems = append(problems, checkOrder(where, entry, schema)...)

		if id != "" && !schema.IDPattern.MatchString(id) {
			problems = append(problems,
				where+": id does not match "+schema.IDPattern.String())
			continue
		}
		if id != "" {
			if prev, dup := seen[id]; dup {
				problems = append(problems,
					fmt.Sprintf("%s: duplicate id, first seen at line %d", where, prev))
			} else {
				seen[id] = lineNo
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s ledger %s has %d structural problem(s):\n  %s",
			schema.Kind, path, len(problems), strings.Join(problems, "\n  "))
	}

	fmt.Printf("Ledger %s (%s): %d entries, %d unique ids, structure clean\n",
		path, schema.Kind, count, len(seen))
	return nil
}

// checkField validates one key, including its absence.
func checkField(where string, entry map[string]any, f fieldSpec, isTerminal bool, schema ledgerSchema) []string {
	v, present := entry[f.Name]
	if !present {
		return []string{where + ": missing key " + f.Name}
	}

	// A key that only applies in the terminal state must be null before
	// it, and carry a real value after. Both directions are derived from
	// status rather than trusted to the author.
	if f.OnlyWhenResolved {
		if !isTerminal {
			if v != nil {
				return []string{fmt.Sprintf(
					"%s: %s must be null while status is not %q", where, f.Name, schema.TerminalKey)}
			}
			return nil
		}
		if v == nil {
			return []string{fmt.Sprintf(
				"%s: %s is required when status is %q", where, f.Name, schema.TerminalKey)}
		}
	}

	if v == nil {
		if f.AllowNull {
			return nil
		}
		return []string{where + ": " + f.Name + " must not be null"}
	}

	if s, ok := v.(string); ok {
		switch s {
		case reservedNotTracked:
			if !f.AllowNA {
				return []string{where + ": " + f.Name + ` may not be "NA"`}
			}
			return nil
		case reservedUndetermined:
			if !f.AllowNaN {
				return []string{where + ": " + f.Name + ` may not be "NaN"`}
			}
			return nil
		}
	}

	switch f.Kind {
	case kindProse, kindEnum:
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return []string{where + ": " + f.Name + " must be a non-empty string"}
		}
		if f.Kind == kindEnum {
			for _, allowed := range f.Enum {
				if s == allowed {
					return nil
				}
			}
			return []string{where + ": " + f.Name + " " + s + " is not one of " +
				strings.Join(f.Enum, ", ")}
		}
	case kindDateTime:
		s, ok := v.(string)
		if !ok {
			return []string{where + ": " + f.Name + " must be an RFC3339 string"}
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return []string{where + ": " + f.Name + " " + s + " is not RFC3339"}
		}
	case kindFiles:
		return checkFiles(where, f.Name, v)
	case kindRefs:
		return checkRefs(where, f.Name, v)
	}
	return nil
}

// checkFiles validates [{path, symbol?, lines?}].
//
// AN EMPTY ARRAY IS A LEGAL AND MEANINGFUL ANSWER: thirty entries name a
// package, a concept or a process rather than a file - "releases",
// "transport ALPN identifiers" - and forcing a path on those would
// invent one.
//
// `symbol` is preferred over `lines` and the reason is measured: 74 of
// 192 entries carried line numbers, and a line number rots on the next
// edit above it while a symbol survives. A gate can also VERIFY a symbol
// still exists; the most it can do for a line is check the file is long
// enough.
func checkFiles(where, name string, v any) []string {
	items, ok := v.([]any)
	if !ok {
		return []string{where + ": " + name + " must be an array (use [] when the entry names no file)"}
	}
	var problems []string
	for i, raw := range items {
		obj, ok := raw.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s[%d] must be an object", where, name, i))
			continue
		}
		for k := range obj {
			if k != "path" && k != "symbol" && k != "lines" {
				problems = append(problems, fmt.Sprintf("%s: %s[%d] unknown key %s", where, name, i, k))
			}
		}
		p, ok := obj["path"].(string)
		if !ok || strings.TrimSpace(p) == "" {
			problems = append(problems, fmt.Sprintf("%s: %s[%d] needs a non-empty path", where, name, i))
		}
		if s, present := obj["symbol"]; present {
			if str, ok := s.(string); !ok || strings.TrimSpace(str) == "" {
				problems = append(problems, fmt.Sprintf("%s: %s[%d] symbol must be a non-empty string", where, name, i))
			}
		}
		if l, present := obj["lines"]; present {
			nums, ok := l.([]any)
			if !ok || len(nums) == 0 {
				problems = append(problems, fmt.Sprintf("%s: %s[%d] lines must be a non-empty array", where, name, i))
				continue
			}
			for _, n := range nums {
				f, ok := n.(float64)
				if !ok || f <= 0 || f != float64(int(f)) {
					problems = append(problems, fmt.Sprintf("%s: %s[%d] lines must be positive integers", where, name, i))
					break
				}
			}
		}
	}
	return problems
}

// checkOrder rejects a terminal timestamp that precedes the opening one.
func checkOrder(where string, entry map[string]any, schema ledgerSchema) []string {
	var start, end string
	switch schema.Kind {
	case "divergence":
		start, end = "opened", "closed"
	case "deprecation":
		start, end = "announced", "removed"
	default:
		return nil
	}
	a, aok := entry[start].(string)
	b, bok := entry[end].(string)
	if !aok || !bok {
		return nil
	}
	ta, erra := time.Parse(time.RFC3339, a)
	tb, errb := time.Parse(time.RFC3339, b)
	if erra != nil || errb != nil {
		return nil // the per-key check already reported the format
	}
	if tb.Before(ta) {
		return []string{fmt.Sprintf("%s: %s %s precedes %s %s", where, end, b, start, a)}
	}
	return nil
}

// checkRefs validates [{ref, rel}].
//
// A ref is a TYPED pointer, and both halves are closed sets. It replaces
// a mechanism rather than adding one: design documents currently find
// the entries bearing on them by string-matching prose, which is why a
// document must be named in `rule` or the query cannot see the entry.
//
// Empty is the normal state. refs fill as entries are touched, because
// three quarters of the references already in prose carry no detectable
// relation - a mechanical backfill would guess, and a ref is worth
// having only when a human chose it.
func checkRefs(where, name string, v any) []string {
	items, ok := v.([]any)
	if !ok {
		return []string{where + ": " + name + " must be an array (use [] when the entry points at nothing)"}
	}
	var problems []string
	for i, raw := range items {
		obj, ok := raw.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s[%d] must be an object", where, name, i))
			continue
		}
		for k := range obj {
			if k != "ref" && k != "rel" {
				problems = append(problems, fmt.Sprintf("%s: %s[%d] unknown key %s", where, name, i, k))
			}
		}
		r, _ := obj["ref"].(string)
		if !refScheme.MatchString(r) {
			problems = append(problems, fmt.Sprintf(
				"%s: %s[%d] ref %q must be scheme:target with scheme in div, dep, doc, decision, note",
				where, name, i, r))
		}
		rel, _ := obj["rel"].(string)
		valid := false
		for _, allowed := range refRels {
			if rel == allowed {
				valid = true
				break
			}
		}
		if !valid {
			problems = append(problems, fmt.Sprintf("%s: %s[%d] rel %q is not one of %s",
				where, name, i, rel, strings.Join(refRels, ", ")))
		}
	}
	return problems
}
