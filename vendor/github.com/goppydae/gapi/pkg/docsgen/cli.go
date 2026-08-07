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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// CLI renders a command tree as relearn-ready markdown, one file per
// command, under dir.
//
// THE ROOT PASSED HERE MUST BE THE POPULATED ONE. gapictl has two: a
// package-level singleton that five func init() blocks add verbs to, and
// NewGapictlRoot, which returns a fresh root carrying only the
// persistent flags and `version`. Measured: the singleton walks to 25
// commands and the constructor to 2. Generating from the constructor
// produces a reference that omits ping, shutdown, tui, and every agent,
// crypto and lifecycle verb - and a drift gate over that output stays
// green forever, because it is comparing a stub to itself. Use
// cli.GetRoot(); see tools/gendocs.
//
// DisableAutoGenTag is set because cobra's tag carries a date. Every
// artifact here is committed and byte-compared, so one clock anywhere
// makes every build dirty.
func CLI(root *cobra.Command, dir string) error {
	if root == nil {
		return fmt.Errorf("docsgen: CLI needs a command root")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("docsgen: creating %s: %w", dir, err)
	}
	disableAutoGenTag(root)

	prepender := func(filename string) string {
		return frontMatter(commandTitle(filename), "", 0)
	}
	// Cobra emits links as "gapictl_agent_build.md"; relearn wants a
	// relative ref to the sibling page.
	linkHandler := func(name string) string {
		return "./" + strings.TrimSuffix(name, ".md") + "/"
	}

	if err := doc.GenMarkdownTreeCustom(root, dir, prepender, linkHandler); err != nil {
		return fmt.Errorf("docsgen: rendering CLI markdown for %s: %w", root.Name(), err)
	}
	return nil
}

// commandTitle turns "gapictl_agent_build.md" into "gapictl agent build".
func commandTitle(filename string) string {
	base := strings.TrimSuffix(filepath.Base(filename), ".md")
	return strings.ReplaceAll(base, "_", " ")
}

// disableAutoGenTag clears the generated-on tag for a command and every
// descendant.
//
// Setting it on the root alone is not enough: cobra reads the field on
// each command as it renders, and a subcommand constructed elsewhere -
// which is all of them here, since they are package-level vars - carries
// its own zero value. The root's setting does not inherit.
func disableAutoGenTag(c *cobra.Command) {
	c.DisableAutoGenTag = true
	for _, sub := range c.Commands() {
		disableAutoGenTag(sub)
	}
}
