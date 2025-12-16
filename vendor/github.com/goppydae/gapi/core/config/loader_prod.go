//go:build !dev

package config

import "github.com/spf13/viper"

func addDefaultPaths() {
	viper.AddConfigPath("/etc/gapi")
}
