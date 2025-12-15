//go:build dev

package config

import "github.com/spf13/viper"

func addDefaultPaths() {
	viper.AddConfigPath("config")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/gapi")
}
