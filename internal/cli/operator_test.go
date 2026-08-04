// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestOperatorKeygenWritesPrivateKeyUnreadableByOthers pins the file mode.
// gapi's SavePrivate creates the file 0644, so without the explicit chmod
// an operator's root-of-trust private key is readable by every user on
// the host. A mode regression is silent - the key still works - so only
// an assertion catches it.
func TestOperatorKeygenWritesPrivateKeyUnreadableByOthers(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "op")

	operatorKeygenOut = prefix
	t.Cleanup(func() { operatorKeygenOut = "" })

	operatorKeygenCmd.SetOut(io.Discard)

	if err := operatorKeygenCmd.RunE(operatorKeygenCmd, nil); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	info, err := os.Stat(prefix + ".key")
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("private key mode is %#o; group and other bits must be clear", perm)
	}

	pubInfo, err := os.Stat(prefix + ".pub")
	if err != nil {
		t.Fatalf("stat public key: %v", err)
	}
	if perm := pubInfo.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("public key mode is %#o; group and other bits must be clear", perm)
	}
}

// TestOperatorKeygenTightensAnExistingLooseFile covers regenerating a key
// at a path that already holds a world-readable file. O_CREATE's mode
// argument does nothing when the file exists, so without an explicit
// chmod before the write the private key bytes land in a 0644 file and
// are only protected after the fact.
func TestOperatorKeygenTightensAnExistingLooseFile(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "op")
	if err := os.WriteFile(prefix+".key", []byte("stale"), 0o644); err != nil {
		t.Fatalf("plant loose file: %v", err)
	}

	operatorKeygenOut = prefix
	t.Cleanup(func() { operatorKeygenOut = "" })
	operatorKeygenCmd.SetOut(io.Discard)
	if err := operatorKeygenCmd.RunE(operatorKeygenCmd, nil); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	info, err := os.Stat(prefix + ".key")
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("private key mode is %#o after regenerating over a loose file; want group and other bits clear", perm)
	}
}
