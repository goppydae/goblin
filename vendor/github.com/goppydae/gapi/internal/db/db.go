// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package db

import (
	"os"

	"go.etcd.io/bbolt"
)

func NewInMemoryDB() (*bbolt.DB, error) {
	tmpFile, err := os.CreateTemp("", "bolt-inmem-")
	if err != nil {
		return nil, err
	}
	// Unlink the file immediately to simulate memory-only
	err = os.Remove(tmpFile.Name())
	if err != nil {
		return nil, err
	}

	return bbolt.Open(tmpFile.Name(), 0600, &bbolt.Options{Timeout: 0})
}
