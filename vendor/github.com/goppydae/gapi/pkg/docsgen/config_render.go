// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package docsgen

import (
	"encoding/json"
	"fmt"
	"strings"
)

// noneMarker is how an empty default is shown.
//
// An empty string rendered as nothing at all is indistinguishable from a
// missing cell, and a reader cannot tell "this defaults to empty" from
// "nobody filled this in" - which is the same ambiguity core/config just
// removed on the code side.
const noneMarker = "(none)"

// DefaultEntry is one key as published in defaults.json.
//
// The field names match magelib's reader exactly. They are the contract
// between the generator and the gate: the gate scans documents for these
// values, and a rename on one side without the other produces a gate
// that silently checks nothing.
type DefaultEntry struct {
	Value  string `json:"value"`
	Env    string `json:"env"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

// DefaultsJSON renders the model as the data file Hugo's `default`
// shortcode reads and magelib's defaults gate scans.
//
// Prose stops transcribing values because of this file: a page writes
// {{< default "transport.address" >}} and Hugo fails the build on an
// unknown key, so a stale quotation cannot survive a rename.
func DefaultsJSON(m *ConfigModel, source string) ([]byte, error) {
	if source == "" {
		return nil, fmt.Errorf("docsgen: defaults.json needs a source naming what defines these values")
	}
	out := make(map[string]DefaultEntry, len(m.Keys))
	for _, k := range m.Keys {
		out[k.Path] = DefaultEntry{
			Value:  k.Value,
			Env:    k.Env,
			Type:   k.Type,
			Source: source,
		}
	}
	// MarshalIndent sorts map keys, so the byte output is stable without
	// a sort here - which the drift gate depends on.
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("docsgen: encoding defaults.json: %w", err)
	}
	return append(data, '\n'), nil
}

// ConfigMarkdown renders the model as the configuration reference page.
func ConfigMarkdown(m *ConfigModel, weight int) []byte {
	var b strings.Builder
	b.WriteString(frontMatter("Configuration",
		"Every configuration key, its default, and its environment override", weight))

	fmt.Fprintf(&b, "Every key below is settable in the configuration file and overridable\n"+
		"from the environment. This page is generated from the same schema and the\n"+
		"same defaults `%s` itself loads, so it cannot disagree with the binary.\n\n", m.Product)

	b.WriteString("| Key | Type | Default | Environment |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, k := range m.Keys {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | `%s` |\n",
			k.Path, k.Type, markdownValue(k.Value), k.Env)
	}

	// The descriptions go below the table rather than in a fifth column:
	// a doc comment is a sentence, and a sentence in a table cell makes
	// every other column unreadable.
	var documented []Key
	for _, k := range m.Keys {
		if k.Doc != "" {
			documented = append(documented, k)
		}
	}
	if len(documented) > 0 {
		b.WriteString("\n## Notes\n\n")
		for _, k := range documented {
			fmt.Fprintf(&b, "**`%s`**\n: %s\n\n", k.Path, k.Doc)
		}
	}
	return []byte(b.String())
}

// markdownValue renders a default for a table cell.
func markdownValue(v string) string {
	if v == "" {
		return noneMarker
	}
	return "`" + v + "`"
}

// ConfigMan renders <product>.conf.5 as roff.
//
// Emitted directly rather than converted from the markdown above.
// go-md2man is vendored here and handles prose well, but a definition
// list is not standard markdown, and round-tripping structured data
// through a prose format to recover structure is what produced the
// roff-shaped homepage this whole programme is replacing.
//
// date is the repo VERSION, never time.Now(): these pages are committed
// and byte-compared, so a clock would make every build dirty.
func ConfigMan(m *ConfigModel, date string) []byte {
	name := m.Product + ".conf"
	var b strings.Builder

	fmt.Fprintf(&b, ".TH %s 5 %q %q %q\n",
		strings.ToUpper(name), date, m.Product, "File Formats Manual")

	b.WriteString(".SH NAME\n")
	fmt.Fprintf(&b, "%s \\- configuration file for %s\n", name, m.Product)

	b.WriteString(".SH DESCRIPTION\n")
	fmt.Fprintf(&b, "Configuration is read from %s in the search path, and every key may be\n",
		roffEscape(name))
	b.WriteString("overridden by the environment variable named with it below.\n")
	b.WriteString(".PP\n")
	b.WriteString("Precedence is environment, then configuration file, then the default.\n")

	b.WriteString(".SH SETTINGS\n")
	for _, k := range m.Keys {
		fmt.Fprintf(&b, ".TP\n.B %s\n", roffEscape(k.Path))
		fmt.Fprintf(&b, "Type: %s.\n", roffEscape(k.Type))
		if k.Value == "" {
			fmt.Fprintf(&b, "Default: %s.\n", noneMarker)
		} else {
			fmt.Fprintf(&b, "Default: %s.\n", roffEscape(k.Value))
		}
		fmt.Fprintf(&b, "Environment: %s.\n", roffEscape(k.Env))
		if k.Doc != "" {
			fmt.Fprintf(&b, ".br\n%s\n", roffEscape(k.Doc))
		}
	}

	b.WriteString(".SH SEE ALSO\n")
	fmt.Fprintf(&b, ".BR %sd (1),\n.BR %sctl (1)\n", m.Product, m.Product)
	return []byte(b.String())
}

// roffEscape makes a value safe inside a roff document.
//
// Two hazards, both silent. A backslash is roff's escape character, so an
// unescaped Windows-style path would swallow the character after it. And
// a line whose FIRST character is a period or an apostrophe is read as a
// roff request rather than text, so a default value that happens to
// start with "." would be interpreted as a macro and vanish from the
// rendered page.
func roffEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\e`)
	s = strings.ReplaceAll(s, "\n", " ")
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "'") {
		s = `\&` + s
	}
	return s
}
