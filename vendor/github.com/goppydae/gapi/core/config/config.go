package config

import (
	"fmt"
	"os"
	"strings"
	"time"

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

type TimeoutConfig struct {
	QUICStream         string `mapstructure:"quicStream"`
	QUICIdle           string `mapstructure:"quicIdle"`
	ClientPending      string `mapstructure:"clientPending"`
	ClientTerminal     string `mapstructure:"clientTerminal"`
	SupervisorStart    string `mapstructure:"supervisorStart"`
	SupervisorShutdown string `mapstructure:"supervisorShutdown"`
}

type SupervisorConfig struct {
	ProductionMode bool `mapstructure:"productionMode"`
}

type Config struct {
	Transport  TransportConfig  `mapstructure:"transport"`
	Security   SecurityConfig   `mapstructure:"security"`
	Metrics    MetricsConfig    `mapstructure:"metrics"`
	Logging    LoggingConfig    `mapstructure:"logging"`
	Timeouts   TimeoutConfig    `mapstructure:"timeouts"`
	Supervisor SupervisorConfig `mapstructure:"supervisor"`
}

func Load() (*Config, error) {
	if env := os.Getenv("RUNTIME_CONFIG"); env != "" {
		viper.SetConfigFile(env)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		addDefaultPaths() // uses build tag-specific implementation
	}
	viper.SetEnvPrefix("RUNTIME")
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

	// Timeout defaults (string format for parsing)
	viper.SetDefault("timeouts.quicStream", QUICStreamTimeout.String())
	viper.SetDefault("timeouts.quicIdle", QUICIdleTimeout.String())
	viper.SetDefault("timeouts.clientPending", ClientPendingTimeout.String())
	viper.SetDefault("timeouts.clientTerminal", ClientTerminalTimeout.String())
	viper.SetDefault("timeouts.supervisorStart", SupervisorStartDeadline.String())
	viper.SetDefault("timeouts.supervisorShutdown", SupervisorShutdownTimeout.String())

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

	// Update global timeouts from config
	if err := updateTimeouts(cfg.Timeouts); err != nil {
		return nil, fmt.Errorf("update timeouts error: %w", err)
	}

	return &cfg, nil
}

func updateTimeouts(t TimeoutConfig) error {
	var err error
	if v, e := time.ParseDuration(t.QUICStream); e == nil {
		QUICStreamTimeout = v
	} else {
		err = e
	}
	if v, e := time.ParseDuration(t.QUICIdle); e == nil {
		QUICIdleTimeout = v
	} else {
		err = e
	}
	if v, e := time.ParseDuration(t.ClientPending); e == nil {
		ClientPendingTimeout = v
	} else {
		err = e
	}
	if v, e := time.ParseDuration(t.ClientTerminal); e == nil {
		ClientTerminalTimeout = v
	} else {
		err = e
	}
	if v, e := time.ParseDuration(t.SupervisorStart); e == nil {
		SupervisorStartDeadline = v
	} else {
		err = e
	}
	if v, e := time.ParseDuration(t.SupervisorShutdown); e == nil {
		SupervisorShutdownTimeout = v
	} else {
		err = e
	}
	return err
}
