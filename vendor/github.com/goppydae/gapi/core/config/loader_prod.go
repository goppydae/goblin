// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

//go:build !dev

package config

import (
	"github.com/spf13/viper"

	"github.com/goppydae/gapi/core/product"
)

func addDefaultPaths(v *viper.Viper) {
	v.AddConfigPath(product.ConfigDir())
}
