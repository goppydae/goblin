// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package store

import (
	"fmt"

	"github.com/goppydae/gapi/internal/db/graphdb"
	"github.com/goppydae/gapi/internal/db/hybriddb"
	"github.com/goppydae/gapi/internal/db/kvdb"
)

type Mode string

const (
	KV     Mode = "kv"
	Graph  Mode = "graph"
	Hybrid Mode = "hybrid"
)

func Open(mode Mode) (Store, error) {
	switch mode {
	case KV:
		return kvdb.New("default")
	case Graph:
		return graphdb.New()
	case Hybrid:
		return hybriddb.New()
	default:
		return nil, fmt.Errorf("unknown store mode: %s", mode)
	}
}
