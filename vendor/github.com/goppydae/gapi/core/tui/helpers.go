// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package tui

import protopkg "github.com/goppydae/gapi/pkg/proto"

func actionToEnum(action string) protopkg.LifecycleControl_Action {
	switch action {
	case "start":
		return protopkg.LifecycleControl_ACTION_START
	case "stop":
		return protopkg.LifecycleControl_ACTION_STOP
	case "restart":
		return protopkg.LifecycleControl_ACTION_RESTART
	case "reload":
		return protopkg.LifecycleControl_ACTION_RELOAD
	default:
		return protopkg.LifecycleControl_ACTION_UNSPECIFIED
	}
}
