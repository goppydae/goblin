//go:build dev

package config

import "github.com/spf13/viper"

func addDefaultPaths(v *viper.Viper) {
	v.AddConfigPath("config")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/gapi")
}
