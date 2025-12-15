package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

type TransportConfig struct {
	Type     string `mapstructure:"type"`
	Address  string `mapstructure:"address"`
	CertFile string `mapstructure:"certFile"`
	KeyFile  string `mapstructure:"keyFile"`
}

type SecurityConfig struct {
	VerifyKey string `mapstructure:"verifyKey"` // Path to public key
}

type Config struct {
	Transport TransportConfig `mapstructure:"transport"`
	Security  SecurityConfig  `mapstructure:"security"`
}

func Load() (*Config, error) {
	if env := os.Getenv("GAPI_CONFIG"); env != "" {
		viper.SetConfigFile(env)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		addDefaultPaths() // uses build tag-specific implementation
	}
	viper.SetEnvPrefix("GAPI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Zero-config defaults
	viper.SetDefault("transport.type", "quic")
	viper.SetDefault("transport.address", ":4242")

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config error: %w", err)
		}
		// Config file not found; proceed with defaults
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}

	return &cfg, nil
}
