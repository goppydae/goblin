// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

// Package docsgen renders reference documentation from the things that
// define it: the cobra command trees, and the configuration schema.
//
// It lives in the kernel because a generator over a command tree is
// MECHANISM, and gapi is the mechanism repo. magelib cannot hold it -
// magelib requires exactly one module, mage, and teaching a build
// library what cobra is would be the wrong trade. goblin cannot hold it
// either, since goblin already resolves its entire configuration schema
// from core/config. So the placement follows the silo's own
// mechanism/policy line rather than cutting across it.
//
// Nothing here reaches for a clock. Every artifact this package writes
// is committed and byte-compared by magelib's drift gate, so a
// timestamp anywhere would make every build dirty. Dates come from the
// repo VERSION, passed in by the caller.
//
// All renderers are pure functions over in-memory models, which is what
// lets them be tested against a synthetic cobra tree and a synthetic
// struct rather than against gapi's real ones.
package docsgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goppydae/gapi/internal/safeio"
)

// writeFile creates the parent directory and writes a generated
// artifact.
//
// 0600 and 0750 rather than the usual 0644 and 0755: nothing here needs
// group or world access, git records only the executable bit so the
// committed artifacts land as 100644 regardless, and the tight mode
// keeps gosec quiet without a carve-out.
//
// The G703 suppressions below are NARROW ON PURPOSE, and they are this
// repo's first inline ones - the house style puts gosec carve-outs in
// the Magefile's Lint call. That style is wrong here: G703 is a path
// traversal rule, gapi handles operator-supplied key and agent paths
// throughout, and switching the rule off repo-wide to accommodate a
// documentation generator would trade a real check for a build tool's
// convenience. Two annotated lines are the smaller blast radius.
//
// What they assert: the tainted value is the output root, which is
// gendocs' own argv supplied by mage - "." for a normal generate, a temp
// directory under the drift gate. safeio.Resolve cleans and absolutises
// it, and there is no privilege boundary here to traverse across.
func writeFile(path string, data []byte) error {
	// Through safeio, the audited chokepoint gapi's lint carve-out names:
	// every variable path is cleaned and made absolute in one place
	// rather than at each call site. The output root here comes from a
	// command-line argument, so it is variable by contract.
	p, err := safeio.Resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { // #nosec G703 -- see writeFile's comment
		return fmt.Errorf("creating %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil { // #nosec G703 -- see writeFile's comment
		return fmt.Errorf("writing %s: %w", p, err)
	}
	return nil
}

// frontMatter renders a relearn front matter block.
//
// title is quoted with %q so a command name containing a quote cannot
// produce invalid YAML, and weight orders the sidebar. There is
// deliberately no date field: see the package comment.
func frontMatter(title, description string, weight int) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %q\n", title)
	if description != "" {
		fmt.Fprintf(&b, "description: %q\n", description)
	}
	if weight != 0 {
		fmt.Fprintf(&b, "weight: %d\n", weight)
	}
	b.WriteString("---\n\n")
	return b.String()
}
