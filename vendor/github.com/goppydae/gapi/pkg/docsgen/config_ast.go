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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fieldDocs reads every struct field's documentation comment in a
// package directory, keyed by "StructName.FieldName".
//
// Reflection cannot see comments - they are not in the type
// information - so the only way to put a key's explanation next to its
// value is to read the source. That is the whole reason this walk
// exists, and it is why the model carries Struct and Field: they are the
// join key, and guessing one from the config path would break on the
// first field whose mapstructure tag differs from its Go name, which is
// most of them.
//
// A field with no comment yields no entry rather than an error. Most
// fields have none today, and refusing to generate documentation until
// every field is commented would mean generating none.
// Files are parsed individually rather than through parser.ParseDir,
// which is deprecated for ignoring build tags. Ignoring them is in fact
// what this walk wants - a field's comment is the same whichever build
// the file belongs to, and core/config has two tag-gated loaders - but
// taking that behaviour deliberately from a supported API beats
// inheriting it from a deprecated one and carving the warning out of
// lint.
func fieldDocs(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("docsgen: reading %s for doc comments: %w", dir, err)
	}

	fset := token.NewFileSet()
	out := map[string]string{}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Test files describe the tests, not the schema.
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		names = append(names, e.Name())
	}
	// Sorted so that two files declaring the same struct name resolve the
	// same way on every run. Nothing in core/config does today; a drift
	// gate comparing bytes should not be the thing that discovers it.
	sort.Strings(names)

	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("docsgen: parsing %s for doc comments: %w", filepath.Join(dir, name), err)
		}
		collectStructDocs(file, out)
	}
	return out, nil
}

// collectStructDocs records the comments on every struct field in a file.
func collectStructDocs(file *ast.File, out map[string]string) {
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, f := range st.Fields.List {
			text := docText(f)
			if text == "" {
				continue
			}
			for _, ident := range f.Names {
				out[ts.Name.Name+"."+ident.Name] = text
			}
		}
		return true
	})
}

// docText prefers the doc comment above a field and falls back to the
// trailing line comment.
//
// Both spellings are in use in core/config - MaxSize carries
// `// MB` on the same line while Pid1Mode has a paragraph above it - and
// a reader of the generated page cannot tell which style the author
// chose, so neither should the generator.
func docText(f *ast.Field) string {
	if f.Doc != nil {
		return flattenComment(f.Doc.Text())
	}
	if f.Comment != nil {
		return flattenComment(f.Comment.Text())
	}
	return ""
}

// flattenComment turns a Go comment block into one line.
//
// Man pages and table cells both want a single line, and a comment
// wrapped at 72 columns would otherwise arrive with its line breaks
// intact and render as ragged text mid-table.
func flattenComment(s string) string {
	fields := strings.Fields(strings.ReplaceAll(s, "\n", " "))
	return strings.Join(fields, " ")
}
