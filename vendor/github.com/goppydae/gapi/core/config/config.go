// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/goppydae/gapi/core/product"
)

type TransportConfig struct {
	Type               string `mapstructure:"type"`
	Address            string `mapstructure:"address"`
	TLSCert            string `mapstructure:"tlsCert"`
	TLSKey             string `mapstructure:"tlsKey"`
	TLSCA              string `mapstructure:"tlsCa"`
	InsecureSkipVerify bool   `mapstructure:"insecureSkipVerify"`
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

type WatchdogConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Device   string `mapstructure:"device"`
	Interval string `mapstructure:"interval"`
}

type ShutdownConfig struct {
	GracePeriod string `mapstructure:"gracePeriod"`
}

type SupervisorConfig struct {
	ProductionMode bool `mapstructure:"productionMode"`
	// Pid1Mode activates the Phase-0 pre-userspace boot sequence
	// (subreaper, PID-1 signals, kmsg, early mounts). Off by default:
	// gapid runs as an ordinary supervisor unless it IS init.
	Pid1Mode bool `mapstructure:"pid1Mode"`
	// NoEarlyMounts skips the mount phase (the OCI runtime owns mounts
	// in a container).
	NoEarlyMounts bool           `mapstructure:"noEarlyMounts"`
	Watchdog      WatchdogConfig `mapstructure:"watchdog"`
	Shutdown      ShutdownConfig `mapstructure:"shutdown"`
}

type Config struct {
	Transport  TransportConfig  `mapstructure:"transport"`
	Security   SecurityConfig   `mapstructure:"security"`
	Metrics    MetricsConfig    `mapstructure:"metrics"`
	Logging    LoggingConfig    `mapstructure:"logging"`
	Timeouts   TimeoutConfig    `mapstructure:"timeouts"`
	Supervisor SupervisorConfig `mapstructure:"supervisor"`
}

// EnvKeyFor renders a dotted config path as the environment variable
// that overrides it: under gapid, "supervisor.pid1Mode" becomes
// GAPI_SUPERVISOR_PID1MODE; under goblind, GOBLIN_SUPERVISOR_PID1MODE.
//
// The prefix was the literal "RUNTIME" until GAPI-DIV-059 and the
// literal "GAPI" until GAPI-DIV-061. Neither could be chosen by the
// process embedding the kernel, so an operator of goblind - which links
// this package as a library - had to configure it under a name belonging
// to a component they are not meant to know exists. It now comes from
// core/product, set once by the binary.
//
// Both renames are HARD - no fallback reads an old spelling, decided by
// the operator. A deployed RUNTIME_CONFIG or, on goblind, a deployed
// GAPI_CONFIG therefore yields default config rather than an error,
// which is why each carries a release note.
func EnvKeyFor(path string) string {
	return product.EnvPrefix() + "_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}

// bindEnvOverrides walks the config struct by its mapstructure tags and
// binds every scalar leaf to its environment variable.
//
// This is the ONLY thing that makes a key reachable from the
// environment, and that is deliberate. viper's Unmarshal builds its
// result from the keys viper already knows, and it learns a key only
// from a config file, a default, or an explicit bind - AutomaticEnv adds
// no keys, it only changes what Get returns for a key already known. So
// every key that happened to lack a SetDefault was unreachable and
// dropped in silence: the whole supervisor section, security.verifyKey,
// and the transport TLS paths. GAPI_SUPERVISOR_PRODUCTIONMODE=true
// produced a daemon with signature enforcement OFF and no error
// (GAPI-DIV-038).
//
// Walking the struct rather than listing keys is the point: a field
// added later is bound because it EXISTS, not because someone
// remembered. That is a structural guarantee, and it is why this
// survived the choice to give every key a default - total defaults would
// have made AutomaticEnv sufficient for reachability, but only for as
// long as a test kept the defaults total. A property that holds because
// of the shape of the code outranks one that holds because a test is
// still watching.
//
// AutomaticEnv is deliberately NOT set alongside it. With both, deleting
// this call changed no observable behaviour and no test went red -
// measured - so the mechanism closing GAPI-DIV-038 had no gate of its
// own. With only this, the reachability tests fail the moment it stops
// covering a leaf.
//
// The environment variable name comes from EnvKeyFor rather than from
// viper composing a prefix with a key replacer, so the name the
// generated reference PRINTS is definitionally the name that WORKS.
func bindEnvOverrides(v *viper.Viper, t reflect.Type, prefix string) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			bindEnvOverrides(v, ft, path)
			continue
		}
		// Maps and slices have no sensible single-variable spelling; a
		// caller wanting logging.loki.labels uses the config file.
		if ft.Kind() == reflect.Map || ft.Kind() == reflect.Slice {
			continue
		}
		_ = v.BindEnv(path, EnvKeyFor(path))
	}
}

// Defaults returns a viper carrying the environment bindings and every
// registered default, with no config file read.
//
// It exists so that the thing which DEFINES the defaults can also be the
// thing that DOCUMENTS them. The configuration reference and the
// <product>.conf.5 man page are generated from this viper joined with a
// reflection walk of Config, so a key cannot appear in the documentation
// without being reachable in the code, or change its value without the
// page changing with it. That is goal 4's claim made mechanical rather
// than promised.
//
// It is product-aware: transport.address, metrics.addr and
// logging.file.path all resolve through core/product, so calling this
// under gapid and under goblind yields the same key set with different
// values - which is what lets one renderer produce both products' pages
// from one schema.
//
// Like Load, this panics on an unset product identity rather than
// guessing one (GAPI-DIV-061).
func Defaults() *viper.Viper {
	// A private instance rather than viper's package-level singleton:
	// the global carried bindings and defaults across every call in a
	// process, which is exactly the hidden global state that makes a
	// configuration bug reproduce only in the second test.
	v := viper.New()
	v.SetEnvPrefix(product.EnvPrefix())
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	bindEnvOverrides(v, reflect.TypeOf(Config{}), "")
	setDefaults(v)
	return v
}

// setDefaults registers a default for EVERY key the config tree exposes.
//
// Totality is the property, and it was not true until now: twelve of the
// thirty-three keys had no SetDefault at all. Nothing was broken by that,
// because bindEnvOverrides binds each key explicitly and is what makes
// it reachable - but "this key has no default" and "this key was
// forgotten" were the same silence, and Defaults() could not describe
// the schema it is now asked to document.
//
// What totality buys is therefore documentation, not reachability. The
// configuration reference and <product>.conf.5 are generated by joining
// this viper with a reflection walk of Config, and a join over two sets
// that are allowed to disagree documents whichever one it happened to
// read. With both total, a key cannot appear in the reference without
// existing in the struct, or exist in the struct without a stated
// default. TestDefaultsRegistersEveryReachableKey and its converse hold
// the two directions.
//
// The twelve added here are registered at the values they ALREADY
// resolve to. Unmarshal yields the zero value for an absent key, and
// every consumer reads them that way - pid1_wiring.go:85 and :96 parse
// the duration strings and skip on error, supervisor.go:83 treats an
// empty verifyKey as "no key". So this declares what happens rather than
// changing it, which is why the behaviour tests are unchanged.
func setDefaults(v *viper.Viper) {
	// Zero-config defaults
	v.SetDefault("transport.type", "quic")
	v.SetDefault("transport.address", product.DefaultControlAddr())
	v.SetDefault("transport.insecureSkipVerify", true)
	v.SetDefault("transport.tlsCert", "")
	v.SetDefault("transport.tlsKey", "")
	v.SetDefault("transport.tlsCa", "")
	v.SetDefault("metrics.enabled", false)
	v.SetDefault("metrics.addr", product.DefaultMetricsAddr())

	// Security. Empty means no key configured, which production mode
	// refuses rather than silently accepting.
	v.SetDefault("security.verifyKey", "")

	// Logging defaults
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.file.enabled", false)
	v.SetDefault("logging.file.path", product.DefaultLogPath())
	v.SetDefault("logging.file.maxSize", 100) // MB
	v.SetDefault("logging.file.maxBackups", 3)
	v.SetDefault("logging.file.maxAge", 28) // days
	v.SetDefault("logging.file.compress", true)
	v.SetDefault("logging.loki.enabled", false)
	v.SetDefault("logging.loki.url", "")

	// Supervisor. Every one of these is off by default: gapid runs as an
	// ordinary supervisor unless it IS init, and the watchdog and grace
	// period are opt-in because an unset duration string fails to parse
	// and is skipped.
	v.SetDefault("supervisor.productionMode", false)
	v.SetDefault("supervisor.pid1Mode", false)
	v.SetDefault("supervisor.noEarlyMounts", false)
	v.SetDefault("supervisor.watchdog.enabled", false)
	v.SetDefault("supervisor.watchdog.device", "")
	v.SetDefault("supervisor.watchdog.interval", "")
	v.SetDefault("supervisor.shutdown.gracePeriod", "")

	// Timeout defaults (string format for parsing)
	v.SetDefault("timeouts.quicStream", QUICStreamTimeout.String())
	v.SetDefault("timeouts.quicIdle", QUICIdleTimeout.String())
	v.SetDefault("timeouts.clientPending", ClientPendingTimeout.String())
	v.SetDefault("timeouts.clientTerminal", ClientTerminalTimeout.String())
	v.SetDefault("timeouts.supervisorStart", SupervisorStartDeadline.String())
	v.SetDefault("timeouts.supervisorShutdown", SupervisorShutdownTimeout.String())
}

func Load() (*Config, error) {
	v := Defaults()

	if env := os.Getenv(product.EnvKey("CONFIG")); env != "" {
		v.SetConfigFile(env)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		addDefaultPaths(v) // uses build tag-specific implementation
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, fmt.Errorf("read config error: %w", err)
		}
		// Config file not found; proceed with defaults
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
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
