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
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/viper"
)

// Key is one operator-settable configuration key, joined from the three
// places that each know part of it.
type Key struct {
	// Path is the dotted config path, e.g. "transport.address".
	Path string
	// Type is the Go type name of the leaf, e.g. "string".
	Type string
	// Doc is the field's doc comment. Reflection cannot see comments, so
	// this comes from an ast walk and is empty when the field has none.
	Doc string
	// Env is the environment variable that overrides this key.
	Env string
	// Value is the resolved default, rendered as a string. Product-aware:
	// the same key yields a different value under gapi and goblin.
	Value string
	// Struct and Field name where the key comes from, so the ast walk can
	// be joined without guessing and a reader can find the declaration.
	Struct string
	Field  string
}

// ConfigModel is the whole schema for one product.
type ConfigModel struct {
	// Product is "gapi" or "goblin".
	Product string
	// Keys are sorted by Path. Sorted rather than in declaration order
	// because a map is walked somewhere upstream of every renderer, and
	// the drift gate compares bytes: an unstable order would make every
	// second build dirty for no reason.
	Keys []Key
}

// ConfigOptions describes where each part of the model comes from.
//
// Every source is injected rather than reached for, which is what lets
// the renderers be tested against a synthetic struct and a synthetic
// viper instead of against gapi's real configuration - the difference
// between testing this package and testing core/config through it.
type ConfigOptions struct {
	// Product is the product name the model is being built for.
	Product string
	// Schema is a zero value of the configuration struct, e.g.
	// &config.Config{}.
	Schema any
	// Defaults is the viper carrying every registered default, i.e.
	// config.Defaults().
	Defaults *viper.Viper
	// SourceDir is the directory holding the schema's Go source, read for
	// doc comments. Empty means no comments are joined.
	SourceDir string
	// EnvKeyFor renders a config path as its environment variable. It is
	// a function rather than a derived string because the composition
	// belongs to core/config, which owns the guarantee that the name it
	// prints is the name that works.
	EnvKeyFor func(path string) string
}

// BuildConfigModel joins the reflection walk, the ast walk and the
// defaults viper into one model.
//
// The three sources are separate because each knows something the others
// cannot: reflection knows the key paths and Go types, the ast knows the
// doc comments, and the viper knows the resolved product-aware values.
// A renderer reading any one of them alone produces a page that is
// correct about a third of what it says.
func BuildConfigModel(o ConfigOptions) (*ConfigModel, error) {
	if o.Schema == nil {
		return nil, fmt.Errorf("docsgen: config model needs a Schema")
	}
	if o.Defaults == nil {
		return nil, fmt.Errorf("docsgen: config model needs a Defaults viper")
	}
	if o.EnvKeyFor == nil {
		return nil, fmt.Errorf("docsgen: config model needs an EnvKeyFor function")
	}

	t := reflect.TypeOf(o.Schema)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("docsgen: Schema must be a struct, got %s", t.Kind())
	}

	var docs map[string]string
	if o.SourceDir != "" {
		var err error
		docs, err = fieldDocs(o.SourceDir)
		if err != nil {
			return nil, err
		}
	}

	m := &ConfigModel{Product: o.Product}
	walkSchema(t, "", func(k Key) {
		k.Env = o.EnvKeyFor(k.Path)
		k.Value = renderValue(o.Defaults.Get(k.Path))
		k.Doc = docs[k.Struct+"."+k.Field]
		m.Keys = append(m.Keys, k)
	})
	sort.Slice(m.Keys, func(i, j int) bool { return m.Keys[i].Path < m.Keys[j].Path })

	if err := checkAgreement(m, o.Defaults); err != nil {
		return nil, err
	}
	return m, nil
}

// walkSchema visits every scalar leaf, mirroring core/config's own
// binding walk so the documented key set and the reachable key set are
// derived the same way.
func walkSchema(t reflect.Type, prefix string, emit func(Key)) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkSchema(ft, path, emit)
			continue
		}
		// Maps and slices are documented as config-file-only, matching
		// core/config's rule that they have no single-variable spelling.
		if ft.Kind() == reflect.Map || ft.Kind() == reflect.Slice {
			continue
		}
		emit(Key{
			Path:   path,
			Type:   ft.String(),
			Struct: t.Name(),
			Field:  f.Name,
		})
	}
}

// checkAgreement fails when the struct and the defaults disagree about
// which keys exist.
//
// THIS IS THE GATE THAT FALLS OUT OF THE JOIN, and it is the reason the
// join is worth doing at all. core/config's own tests hold the same
// property from the other side; this holds it at the moment of
// GENERATION, so a reference page cannot be produced that documents a
// key an operator cannot set, or omit one they can.
//
// viper folds keys to lower case, so both sides are compared folded - a
// mapstructure tag of "maxBackups" is stored as "maxbackups", and a
// case-sensitive comparison reports the entire schema as mismatched and
// reads like a total failure rather than a spelling difference.
func checkAgreement(m *ConfigModel, v *viper.Viper) error {
	documented := map[string]bool{}
	for _, k := range m.Keys {
		documented[strings.ToLower(k.Path)] = true
	}

	var undocumented []string
	for _, key := range v.AllKeys() {
		if !documented[strings.ToLower(key)] {
			undocumented = append(undocumented, key)
		}
	}
	sort.Strings(undocumented)
	if len(undocumented) > 0 {
		return fmt.Errorf(
			"docsgen: %d key(s) have a default but no struct field, so they would be documented as settable and silently discarded: %s",
			len(undocumented), strings.Join(undocumented, ", "))
	}
	return nil
}

// renderValue spells a resolved default for documentation.
//
// An absent value renders as the empty string rather than Go's "<nil>",
// and the renderers show that as "(none)". The distinction matters: a
// key whose default is genuinely empty - every TLS path here - is not
// the same claim as a key with no default at all, which core/config no
// longer permits.
func renderValue(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}
