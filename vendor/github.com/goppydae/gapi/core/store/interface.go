// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package store

import (
	"github.com/goppydae/gapi/internal/db/graphdb"
)

type Store interface {
	Close() error
}

type KVStore interface {
	Store
	Set(bucket, key string, value interface{}) error
	Get(bucket, key string, target interface{}) error
	Delete(bucket, key string) error
	Keys(bucket string) ([]string, error)
}

type GraphStore interface {
	Store
	AddNode(n graphdb.Node) error
	AddEdge(e graphdb.Edge) error
	Neighbors(id string, kind string) ([]graphdb.Edge, error)
	ShortestPath(start, end, kind string, ttl int64) ([]string, int, error)
	GetStoredPath(start, end, kind string) (*graphdb.Path, error)
}

type HybridStore interface {
	Store
	Set(bucket, key string, value interface{}) error
	Get(bucket, key string, target interface{}) error
	Delete(bucket, key string) error
	Keys(bucket string) ([]string, error)
	AddNode(n graphdb.Node) error
	AddEdge(e graphdb.Edge) error
	Neighbors(id string, kind string) ([]graphdb.Edge, error)
	ShortestPath(start, end, kind string, ttl int64) ([]string, int, error)
	GetStoredPath(start, end, kind string) (*graphdb.Path, error)
}
