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

type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Addr    string `mapstructure:"addr"`
}

type FileOutputConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	Path       string `mapstructure:"path"`
	MaxSize    int    `mapstructure:"maxSize"`    // MB
	MaxBackups int    `mapstructure:"maxBackups"` // Number of old files to keep
	MaxAge     int    `mapstructure:"maxAge"`     // Days
	Compress   bool   `mapstructure:"compress"`
}

type LokiOutputConfig struct {
	Enabled bool              `mapstructure:"enabled"`
	URL     string            `mapstructure:"url"`
	Labels  map[string]string `mapstructure:"labels"`
}

type LoggingConfig struct {
	Level  string           `mapstructure:"level"`  // trace, debug, info, warn, error
	Format string           `mapstructure:"format"` // json, console
	File   FileOutputConfig `mapstructure:"file"`
	Loki   LokiOutputConfig `mapstructure:"loki"`
}

type Config struct {
	Transport TransportConfig `mapstructure:"transport"`
	Security  SecurityConfig  `mapstructure:"security"`
	Metrics   MetricsConfig   `mapstructure:"metrics"`
	Logging   LoggingConfig   `mapstructure:"logging"`
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
	viper.SetDefault("metrics.enabled", false)
	viper.SetDefault("metrics.addr", "127.0.0.1:9090")

	// Logging defaults
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")
	viper.SetDefault("logging.file.enabled", false)
	viper.SetDefault("logging.file.path", "/var/log/gapi/gapi.log")
	viper.SetDefault("logging.file.maxSize", 100) // MB
	viper.SetDefault("logging.file.maxBackups", 3)
	viper.SetDefault("logging.file.maxAge", 28) // days
	viper.SetDefault("logging.file.compress", true)
	viper.SetDefault("logging.loki.enabled", false)

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
