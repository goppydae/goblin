// Copyright (c) 2026 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package magelib

import (
	"fmt"
	"strings"
)

// The notice text lives HERE, in the library, and not in any consumer's
// Magefile. That is not tidiness - it is the lesson the terminology
// gate paid for: moving a content gate into a shared library moves the
// LOGIC, while the literals stay behind in the consumer's Magefile,
// which is itself walked. There the cost was a gate that flagged its
// own rule declaration. Here it would be four repos each carrying a
// hand-copied notice, drifting a word at a time, with the gate
// certifying each repo against its own copy - so a repo could pass
// while spelling the licence differently from every other. A consumer
// declares a holder and a year and nothing else.
//
// mplExhibitA is Exhibit A of the Mozilla Public License 2.0, verbatim,
// pre-wrapped. Section 1.4 defines Covered Software as the Source Code
// Form to which this notice HAS BEEN ATTACHED, which is why the wording
// is not ours to improve: a paraphrase attaches nothing. The line
// breaks are presentation and may be rewrapped; the words may not.
var mplExhibitA = []string{
	"This Source Code Form is subject to the terms of the Mozilla Public",
	"License, v. 2.0. If a copy of the MPL was not distributed with this",
	"file, You can obtain one at https://mozilla.org/MPL/2.0/.",
}

// mplSPDX is the machine-readable half of the notice. Exhibit A serves
// the human who receives a single file; this serves every scanner
// (ScanCode, FOSSA, licensee, REUSE). Operator decision 17 takes both
// rather than choosing, because they answer to different readers.
const mplSPDX = "SPDX-License-Identifier: MPL-2.0"

// licenseCommentPrefix maps an extension to its line-comment marker,
// and IS the file-type scope from operator decision 18: a file type
// absent from this map is not policed, so adding a language here is the
// whole of teaching the gate about it.
//
// Deliberately absent, each for a stated reason (decision 18): .json
// and .jsonl have no comment syntax at all, so the divergence ledgers
// cannot carry a notice; .yaml, go.mod, go.sum and flake.lock have
// comment syntax but are statements of fact about the build rather than
// creative expression; .md is prose covered by the repo LICENSE. Only
// line comments are used - a block comment would have to be closed, and
// an unterminated insert breaks a file in a way a walk cannot see.
var licenseCommentPrefix = map[string]string{
	".go":    "//",
	".proto": "//",
	".py":    "#",
	".sh":    "#",
	".nix":   "#",
}

// LicenseConfig is everything a repo declares. Two fields, because
// every other part of the notice is the licence's and not the repo's.
type LicenseConfig struct {
	// Holder is the copyright holder, exactly as it should appear
	// after the year.
	Holder string
	// Year is the year of FIRST PUBLICATION for this repo - a single
	// year, never a range, and never the current year.
	//
	// A range means editing ~400 files every January for no legal gain.
	// Stamping the current year per file is worse: the gate would then
	// have to accept any year, which means it could no longer tell a
	// correct notice from a mangled one, and a gate that accepts
	// anything in a field has stopped checking that field. One constant
	// per repo keeps the notice exact-matchable, which is what makes
	// the check discriminating at all.
	Year int
}

// validate rejects a configuration the gate could not enforce
// meaningfully. Each rejection is an ERROR rather than a default,
// because a gate that silently substitutes a value checks something
// other than what the operator declared.
func (c LicenseConfig) validate() error {
	if strings.TrimSpace(c.Holder) == "" {
		return fmt.Errorf("license check: no copyright holder configured; the notice cannot be written without one")
	}
	// ASCII-only is a repo-wide rule, and the copyright SIGN is the one
	// character that reaches for a notice specifically. Caught here it
	// is one clear error; uncaught it is a non-ASCII byte written into
	// every file in the repo by a tool that touched 400 of them.
	for i := 0; i < len(c.Holder); i++ {
		if c.Holder[i] > 0x7e || c.Holder[i] < 0x20 {
			return fmt.Errorf(
				"license check: holder %q contains a non-ASCII or control byte at offset %d; use the ASCII form (c), never the copyright sign",
				c.Holder, i)
		}
	}
	// The bound is loose on purpose. It is not trying to know the right
	// year - only to reject a zero value or a transposed digit, which
	// are the two ways this field goes wrong silently.
	if c.Year < 1990 || c.Year > 2200 {
		return fmt.Errorf(
			"license check: year %d is not a plausible year of first publication", c.Year)
	}
	return nil
}

// noticeLines renders the notice as bare text, without any comment
// syntax. The blank line between each part is load-bearing for a
// reader, and it is what the comment-prefixed form turns into a bare
// "//" or "#".
func (c LicenseConfig) noticeLines() []string {
	lines := []string{
		fmt.Sprintf("Copyright (c) %d %s", c.Year, c.Holder),
		"",
	}
	lines = append(lines, mplExhibitA...)
	return append(lines, "", mplSPDX)
}

// Notice renders the configured notice as a comment block in the style
// of the named extension, with a trailing newline and no surrounding
// blank lines. It is exported so a consumer can print what the gate
// expects without having to run the gate and read an error - and so a
// test can compare against the same string the inserter writes, rather
// than against a second copy that could drift from it.
//
// An unknown extension is an ERROR, not an empty string. A caller that
// received "" would write a file with no notice and no complaint.
func (c LicenseConfig) Notice(ext string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	prefix, ok := licenseCommentPrefix[ext]
	if !ok {
		return "", fmt.Errorf("license check: no comment syntax known for extension %q", ext)
	}
	var b strings.Builder
	for _, line := range c.noticeLines() {
		if line == "" {
			// A bare marker, with no trailing space. gofmt preserves a
			// trailing space inside a comment, so emitting one would
			// make the notice differ from itself by whitespace nobody
			// can see, and an exact-match check would fail on files it
			// had itself written.
			b.WriteString(prefix)
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix)
		b.WriteString(" ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// hasNotice reports whether data already carries the notice in its
// LEADING region - the run of shebang, blank and comment lines before
// the first line of code.
//
// The region matters. A whole-file search would accept a notice pasted
// anywhere, including inside a string literal or a doc comment
// discussing licensing, and this gate's entire job is to certify that a
// file's licence status is unambiguous to a reader who opens it. A
// reader forms that judgement from the top of the file.
func hasNotice(data []byte, prefix, notice string) bool {
	return strings.Contains(leadingRegion(string(data), prefix), notice)
}

// leadingRegion returns the prefix of src consisting of lines that are
// blank, a first-line shebang, or comments in the given style. It stops
// at the first line of code, which is what bounds hasNotice.
func leadingRegion(src, prefix string) string {
	var b strings.Builder
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		isShebang := i == 0 && strings.HasPrefix(line, "#!")
		if trimmed != "" && !isShebang && !strings.HasPrefix(trimmed, prefix) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// insertNotice returns data with the notice inserted, and is ONE rule
// covering all three placement traps a header sweep has:
//
//	[shebang, if line 1 has one] + notice + BLANK LINE + the rest
//
// Each trap falls out of it rather than being special-cased, which is
// why the rule is stated as one thing:
//
//   - SHEBANG. A "#!" is only a shebang on line 1, so it is the only
//     thing that may precede the notice. Prepending above it silently
//     turns an executable script into a text file.
//   - GO BUILD TAGS. A "//go:build" line may be preceded by blank lines
//     and other line comments, so a notice ABOVE it is legal and leaves
//     the constraint in force. Inserting BELOW it - between the tag and
//     the package clause - is what silently disables the constraint,
//     and this rule never does that because it never inserts below
//     anything but a shebang.
//   - GO DOC COMMENTS. A comment block touching "package X" with no
//     blank line between them IS the package doc comment. Prepending
//     without the blank line merges the two and the licence becomes the
//     package documentation, visible in go doc and on pkg.go.dev. The
//     blank line is the whole defence, and it is not conditional on
//     having detected a doc comment - a detector could be wrong, while
//     an unconditional blank line cannot be.
//
// MEASURED before this was written: 34 files in the silo have a comment
// block directly above their package clause (gapi 22, goblin 12).
func insertNotice(data []byte, notice string) []byte {
	src := string(data)
	if strings.TrimSpace(src) == "" {
		return []byte(notice)
	}
	var shebang string
	if strings.HasPrefix(src, "#!") {
		end := strings.Index(src, "\n")
		if end < 0 {
			// A shebang and nothing else. Terminate it rather than
			// letting the notice run onto the same line.
			return []byte(src + "\n" + notice)
		}
		shebang, src = src[:end+1], src[end+1:]
	}
	// Leading blank lines in the remainder would stack with the blank
	// line the rule emits. Dropping them keeps the output identical
	// whether or not the original had them, which is what lets the
	// check compare against an exact string.
	src = strings.TrimLeft(src, "\n")
	return []byte(shebang + notice + "\n" + src)
}
