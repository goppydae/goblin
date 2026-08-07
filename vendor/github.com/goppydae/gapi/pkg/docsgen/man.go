// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package docsgen

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cpuguy83/go-md2man/v2/md2man"
	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// Man renders section 1 pages for a command tree into dir.
//
// version is the repo VERSION and becomes the page's date.
//
// THE DATE IS THE ONLY TRAP IN SECTION 1, and it is silent. Cobra's
// GenManHeader stamps time.Now() when Date is nil, so every regeneration
// produces different bytes and a committed man page is dirty the moment
// anyone rebuilds - which under a byte-comparing drift gate reads as
// "the CLI changed" on a tree where nothing changed. Passing a date
// derived from VERSION makes the page a function of the source, which is
// what the gate assumes it already is.
//
// A man page's date field conventionally carries a date rather than a
// version, and this deliberately does not. The field's job here is to
// identify WHICH BUILD the page describes, and a version answers that
// where a build date answers when someone happened to run the generator.
func Man(root *cobra.Command, dir, version string) error {
	if root == nil {
		return fmt.Errorf("docsgen: Man needs a command root")
	}
	if version == "" {
		return fmt.Errorf("docsgen: Man needs a version; an empty date makes cobra stamp the clock")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("docsgen: creating %s: %w", dir, err)
	}
	disableAutoGenTag(root)

	// Cobra takes a *time.Time, so the injected value has to be one.
	// The epoch is used as a carrier and never rendered as a date: the
	// header's date column is the version string below.
	date := time.Unix(0, 0).UTC()
	header := &doc.GenManHeader{
		Title:   root.Name(),
		Section: "1",
		Date:    &date,
		Source:  root.Name() + " " + version,
		Manual:  "General Commands Manual",
	}
	if err := doc.GenManTree(root, header, dir); err != nil {
		return fmt.Errorf("docsgen: rendering man pages for %s: %w", root.Name(), err)
	}
	return nil
}

// Overview converts a hand-written markdown page into a section 7 roff
// page.
//
// Section 7 is the one place in the reference surface that is WRITTEN
// rather than generated: "what is this system and how do its parts fit"
// is not derivable from a command tree, and pretending otherwise is what
// produced today's Docs:Man, which runs a prose converter over
// docs/index.md and emits a roff-shaped homepage.
//
// The conversion goes through go-md2man, which is already vendored here
// because cobra's own man generator uses it. That is what lets pandoc
// leave both flakes: one roff path serves sections 1, 5 and 7, and the
// toolchain loses a system binary rather than gaining a purpose for it.
func Overview(srcMarkdown, outRoff, product, version string) error {
	if version == "" {
		return fmt.Errorf("docsgen: Overview needs a version for the page header")
	}
	body, err := os.ReadFile(srcMarkdown) // #nosec G304 -- caller-supplied generator input path
	if err != nil {
		return fmt.Errorf("docsgen: reading overview %s: %w", srcMarkdown, err)
	}

	// md2man renders the body; the .TH header is ours, so the section and
	// the date are not left to whatever the markdown happened to start
	// with.
	header := fmt.Sprintf(".TH %s 7 %q %q %q\n",
		strings.ToUpper(product), version, product+" "+version, "Miscellaneous Information Manual")
	out := append([]byte(header), md2man.Render(stripFrontMatter(body))...)
	return writeFile(outRoff, out)
}

// stripFrontMatter removes a leading YAML front matter block.
//
// The source is Hugo content, so it carries front matter that is
// meaningful to the site and meaningless to roff - md2man would render
// the three dashes and the title as a horizontal rule and a stray line
// at the top of the man page.
func stripFrontMatter(b []byte) []byte {
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return b
	}
	if end := strings.Index(s[4:], "\n---\n"); end >= 0 {
		return []byte(strings.TrimLeft(s[4+end+5:], "\n"))
	}
	return b
}
