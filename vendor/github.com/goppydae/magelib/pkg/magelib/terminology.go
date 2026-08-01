package magelib

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TerminologyRule is one phrase that must not appear in a repo, plus
// why. Reason is printed on a hit so whoever trips it does not have to
// go archaeology.
type TerminologyRule struct {
	Phrase string
	Reason string
	// FoldCase matches case-insensitively. Safe for a rule whose phrase
	// is wrong in every casing; NOT safe for a rule that exists to
	// enforce a specific casing, because folding would make it match the
	// canonical form and fail on correct text.
	FoldCase bool
	// WordBoundary requires non-word characters either side, so the rule
	// governs prose and leaves identifiers alone - a styling rule should
	// not force a Go symbol to be renamed.
	WordBoundary bool
}

// GoppydaeTerminologyRules are the silo's naming rules, exported so a
// consumer's Magefile can enable the gate without spelling the
// forbidden phrases itself. That matters: a Magefile sits at the repo
// root and IS walked, so a consumer that declared these inline would
// trip the gate on its own rule declaration, and the only escapes -
// excluding the Magefile or fragmenting the literals - are the ones
// this design exists to avoid. Here the literals reach a consumer
// inside vendor/, which skipDirs already skips.
//
// This does move the self-flagging problem into magelib itself:
// terminology.go and terminology_test.go both spell the forbidden
// phrases in the clear. That is a recorded, deliberate decision and is
// currently harmless because magelib does not run this gate on its own
// tree. If magelib ever wires CheckTerminology into its own Lint
// target, these two files need the inline allow marker below, not a
// file-level skip.
//
// GAPI is a single-node supervision kernel, not a programming
// interface. The product name carries a double P for the GoPPy stack
// (Go + Protobuf + Python).
var GoppydaeTerminologyRules = []TerminologyRule{
	{
		Phrase:   "Agent Programming Interface",
		Reason:   "GAPI expands to 'Agent Process Infrastructure'",
		FoldCase: true,
	},
	{
		Phrase:       "Goppydae",
		Reason:       "canonical styling is 'GoPPydae' - double P, for the GoPPy stack",
		WordBoundary: true,
	},
}

// isWordByte reports whether c is what RE2's \b treats as a word
// character: ASCII letters, digits, underscore.
func isWordByte(c byte) bool {
	return c == '_' ||
		(c >= '0' && c <= '9') ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z')
}

// compile turns a rule into a matcher. Internal whitespace becomes a
// separator that spans a newline, so a phrase that wrapped still
// matches: the docs this polices are hard-wrapped, and a
// contiguous-bytes match would miss the accidental path it exists to
// catch.
func (r TerminologyRule) compile() (*regexp.Regexp, error) {
	parts := strings.Fields(r.Phrase)
	if len(parts) == 0 {
		return nil, fmt.Errorf("terminology rule has an empty phrase")
	}
	// A phrase that contains the allow marker waives itself on every
	// line it could ever match, so the rule is dead on arrival and says
	// nothing about it. Same argument as the WordBoundary guard below:
	// a gate that cannot fire is indistinguishable from a clean tree.
	if strings.Contains(r.Phrase, terminologyAllow) {
		return nil, fmt.Errorf(
			"terminology rule %q contains the allow marker %q and could never fire",
			r.Phrase, terminologyAllow)
	}
	// The boundary decision is about the WORDS, not about how the
	// literal was typed: strings.Fields already dropped any surrounding
	// whitespace, so indexing r.Phrase would judge bytes that never
	// reach the pattern. "Goppydae " would silently lose its trailing
	// \b, and " Goppydae " would be falsely rejected as unmatchable.
	firstWord, lastWord := parts[0], parts[len(parts)-1]
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	// Whitespace between words also crosses a line-continuation
	// prefix, so a phrase wrapped inside a Go comment, a markdown
	// blockquote or a C block comment still matches - hard-wrapped
	// comments are the likeliest place a banned phrase reappears.
	//
	// The prefix must follow a newline, and '-' and '|' are excluded:
	// at line start those are markdown bullets and table cells, not
	// continuations, and including them made three consecutive list
	// items match as one phrase. Measured over these repos, that is a
	// 431-line false-positive surface for '-' against zero occurrences
	// of a '* ' bullet, so the asymmetry is prevalence, not taste.
	//
	// The two branches are mutually exclusive at any position - the
	// first requires a [*/#>] byte, the second is whitespace-only - so
	// their order does not affect matching. Do not add an ordering
	// rationale here: an earlier version claimed \s+ would "swallow"
	// the newline and starve the prefix branch, which is not how Go's
	// regexp works and is not true of these branches anyway.
	const sep = `(?:\s*\n[ \t]*[*/#>]+[ \t]*|\s+)`
	pat := strings.Join(parts, sep)
	if r.WordBoundary {
		// \b is only meaningful next to a word character. Bolting it on
		// unconditionally makes a phrase like "GAPI (Agent Programming
		// Interface)" compile to a pattern that requires a word
		// character after ')' - it can never match, and it does so
		// silently, which is the worst failure mode a gate has.
		first := firstWord[0]
		last := lastWord[len(lastWord)-1]
		if !isWordByte(first) && !isWordByte(last) {
			return nil, fmt.Errorf(
				"terminology rule %q: WordBoundary on a phrase with non-word characters at both ends would never match",
				r.Phrase)
		}
		if isWordByte(first) {
			pat = `\b` + pat
		}
		if isWordByte(last) {
			pat = pat + `\b`
		}
	}
	if r.FoldCase {
		pat = `(?i)` + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("terminology rule %q: %w", r.Phrase, err)
	}
	return re, nil
}

// terminologySkipNames are never walked, on top of the shared skipDirs.
// Callers add their own - a divergence ledger quotes the phrases it is
// about, so a gate that walked it would go red against its own
// justification.
var terminologySkipNames = map[string]bool{
	"node_modules": true, "dist": true, "result": true,
}

// maxTerminologyFileSize bounds what is worth reading as prose. Anything
// larger is a build artifact or a generated blob.
const maxTerminologyFileSize = 1 << 20

// terminologyAllow, present anywhere on the line a match starts, waives
// that match. Precedent is gosec's #nosec. Without it the only escape
// is unpolicing a whole file, and prose that quotes the rule it
// explains - a lexicon, a CONTRIBUTING entry - has to be allowed to say
// the wrong phrase once.
const terminologyAllow = "terminology:allow"

// CheckTerminology fails when any rule matches a text file in the repo
// rooted at the current directory. It is meant to be wired into a Lint
// target so it rides a CI context that already blocks merges, rather
// than becoming a target nobody invokes.
//
// skips are paths and base names this gate does not walk, each with the
// reason the rule does not reach it. Their shapes and the errors they
// can be rejected with are Skip's, not this gate's: the validation is
// shared with every other gate through compileSkips, so a skip that
// cannot mean what the caller intended is rejected once, for all of
// them.
//
// A single line may waive its own match with the marker
// "terminology:allow" (see terminologyAllow), which is the granular
// alternative to skipping a whole file.
//
// Unreadable files are an ERROR, not a skip: a gate that cannot read a
// file must not report it clean.
func CheckTerminology(rules []TerminologyRule, skips ...Skip) error {
	if len(rules) == 0 {
		return fmt.Errorf("terminology check: no rules configured")
	}
	matchers := make([]*regexp.Regexp, len(rules))
	for i, r := range rules {
		re, err := r.compile()
		if err != nil {
			return err
		}
		matchers[i] = re
	}
	skipPaths, skipNames, err := compileSkips("terminology check", skips)
	if err != nil {
		return err
	}
	for k, v := range terminologySkipNames {
		skipNames[k] = v
	}

	var hits []string
	var waived int
	err = filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if skipPaths[filepath.Clean(path)] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if skipDirs[d.Name()] || skipNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("terminology check: stat %s: %w", path, err)
		}
		if !info.Mode().IsRegular() || info.Size() > maxTerminologyFileSize {
			return nil
		}
		// #nosec G122,G304 -- walking the repo we were invoked in; WalkDir
		// does not follow symlinks and non-regular files are skipped above.
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("terminology check: read %s: %w", path, err)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil // binary
		}
		for i, re := range matchers {
			// One hit per rule per file is enough to fail the gate, but a
			// waived hit must not mask a later real one, so keep scanning
			// past anything the allow marker excuses.
			for off := 0; off < len(data); {
				loc := re.FindIndex(data[off:])
				if loc == nil {
					break
				}
				start, end := off+loc[0], off+loc[1]
				// A wrapped match spans lines. The marker is looked for
				// across the region from the start of the line holding the
				// match START through the end of the line holding its end,
				// so putting the marker on the first line of a wrapped
				// occurrence - where a human reads the sentence begin - is
				// the intended way to waive it.
				lineStart := bytes.LastIndexByte(data[:start], '\n') + 1
				lineEnd := bytes.IndexByte(data[end:], '\n')
				if lineEnd < 0 {
					lineEnd = len(data)
				} else {
					lineEnd += end
				}
				if bytes.Contains(data[lineStart:lineEnd], []byte(terminologyAllow)) {
					waived++
					off = end
					continue
				}
				// Suppression is line-scoped, so the operator needs the
				// line to place the marker; on a hard-wrapped doc the
				// matched bytes alone are not enough to find it.
				line := bytes.Count(data[:start], []byte("\n")) + 1
				hits = append(hits, fmt.Sprintf("  %s:%d: %q\n      %s",
					path, line, data[start:end], rules[i].Reason))
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		return fmt.Errorf("terminology check failed (%d waived):\n%s", waived, strings.Join(hits, "\n"))
	}
	// gosec, the precedent for the inline marker, prints "Nosec: N".
	// Without the same, a file whose only occurrence is waived reads
	// exactly like a clean one and waivers accumulate unreviewed.
	if waived > 0 {
		fmt.Printf("terminology check: clean (%d waived)\n", waived)
	}
	return nil
}
