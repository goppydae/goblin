// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

import (
	"github.com/goppydae/gapi/core/eventbus"

	"google.golang.org/protobuf/types/known/anypb"
)

// All transports are now strictly protobuf-based.
type Transport = eventbus.Transport[*anypb.Any]
