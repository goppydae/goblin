// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import "math"

// Bus payloads cross the cluster as JSON (numbers arrive as float64)
// but are delivered locally with their original Go types. These
// helpers normalize both paths with explicit bounds; anything
// unrecognized or out of range reads as zero - a dropped locator field
// is recoverable, a silently wrapped one is not.

func payloadInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case uint64:
		if n > math.MaxInt64 {
			return 0
		}
		return int64(n)
	case uint32:
		return int64(n)
	case float64:
		if n < math.MinInt64 || n > math.MaxInt64 {
			return 0
		}
		return int64(n)
	default:
		return 0
	}
}

// payloadUint64 reads a non-negative numeric payload field.
func payloadUint64(v interface{}) uint64 {
	n := payloadInt64(v)
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// payloadUint32 reads a small non-negative numeric payload field.
func payloadUint32(v interface{}) uint32 {
	n := payloadInt64(v)
	if n < 0 || n > math.MaxUint32 {
		return 0
	}
	return uint32(n)
}

func payloadString(v interface{}) string {
	s, _ := v.(string)
	return s
}
