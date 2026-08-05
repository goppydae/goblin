// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package crypto

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
	"github.com/goppydae/gapi/internal/safeio"
)

// GenerateAgeIdentity generates a new x25519 identity.
func GenerateAgeIdentity() (*age.X25519Identity, error) {
	return age.GenerateX25519Identity()
}

// EncryptAge encrypts data for a list of recipients (public keys).
func EncryptAge(recipients []string, data []byte) ([]byte, error) {
	var parsed []age.Recipient
	for _, r := range recipients {
		p, err := age.ParseX25519Recipient(r)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient %q: %w", r, err)
		}
		parsed = append(parsed, p)
	}

	out := &bytes.Buffer{}
	w, err := age.Encrypt(out, parsed...)
	if err != nil {
		return nil, fmt.Errorf("encrypt init: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return nil, fmt.Errorf("encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("encrypt close: %w", err)
	}

	return out.Bytes(), nil
}

// DecryptAge decrypts data using an identity file (containing private keys).
func DecryptAge(identityPath string, data []byte) ([]byte, error) {
	// Read identities from file
	f, err := safeio.Open(identityPath)
	if err != nil {
		return nil, fmt.Errorf("open identity: %w", err)
	}

	ids, err := age.ParseIdentities(f)
	if cerr := f.Close(); cerr != nil && err == nil {
		err = fmt.Errorf("close identity: %w", cerr)
	}
	if err != nil {
		return nil, fmt.Errorf("parse identities: %w", err)
	}

	// Decrypt
	r, err := age.Decrypt(bytes.NewReader(data), ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	out := &bytes.Buffer{}
	if _, err := io.Copy(out, r); err != nil {
		return nil, fmt.Errorf("decrypt read: %w", err)
	}

	return out.Bytes(), nil
}

// WriteAgeIdentity writes an identity to a file in standard format.
//
// The identity is a secret, so the file is replaced rather than written
// through - see safeio.ReplaceOwnerOnly. os.WriteFile's perm argument would
// apply only when creating the file, leaving an overwrite at whatever mode the
// destination already had.
func WriteAgeIdentity(path string, id *age.X25519Identity) error {
	content := fmt.Sprintf("# public key: %s\n%s\n",
		id.Recipient().String(),
		id.String(),
	)
	return safeio.ReplaceOwnerOnly(path, []byte(content))
}
