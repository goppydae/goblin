//go:build dev

package config

import (
	"github.com/spf13/viper"

	"github.com/goppydae/gapi/core/product"
)

func addDefaultPaths(v *viper.Viper) {
	v.AddConfigPath("config")
	v.AddConfigPath(".")
	v.AddConfigPath(product.ConfigDir())
}
