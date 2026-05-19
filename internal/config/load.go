package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// LoadOptions controls Load's behaviour.
type LoadOptions struct {
	// ConfigPath, if non-empty, is the explicit YAML file to load.
	// If empty, no file is read (defaults + env still apply).
	ConfigPath string

	// FlagBinder is an optional hook that lets a caller bind cobra flags
	// onto the underlying viper instance. Called once, after defaults are
	// set and before the config file is read.
	FlagBinder func(v *viper.Viper) error
}

// Load reads configuration with precedence:
//
//	flag > env (BNS_*) > YAML file > defaults
//
// Env vars use prefix "BNS_" and double-underscore "__" as the nesting
// separator (e.g. BNS_LISTEN__UDP -> listen.udp).
func Load(opts LoadOptions) (Config, error) {
	v := viper.New()
	v.SetEnvPrefix("BNS")
	// AutomaticEnv calls the replacer on the viper key to produce the env var
	// name. We want listen.udp -> BNS_LISTEN__UDP, so replace "." with "__".
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	bindDefaults(v, Default())

	if opts.FlagBinder != nil {
		if err := opts.FlagBinder(v); err != nil {
			return Config{}, fmt.Errorf("bind flags: %w", err)
		}
	}

	if opts.ConfigPath != "" {
		v.SetConfigFile(opts.ConfigPath)
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return Config{}, fmt.Errorf("read config %q: %w", opts.ConfigPath, err)
			}
		}
	}

	cfg := Default()
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

// bindDefaults walks the Default() struct and registers each value with
// viper.SetDefault so that env/flag overrides compose correctly.
func bindDefaults(v *viper.Viper, d Config) {
	v.SetDefault("listen.udp", d.Listen.UDP)
	v.SetDefault("listen.tcp", d.Listen.TCP)
	v.SetDefault("listen.query_timeout", d.Listen.QueryTimeout)

	v.SetDefault("cache.capacity", d.Cache.Capacity)
	v.SetDefault("cache.min_ttl", d.Cache.MinTTL)
	v.SetDefault("cache.max_ttl", d.Cache.MaxTTL)
	v.SetDefault("cache.negative_ttl_max", d.Cache.NegativeTTLMax)
	v.SetDefault("cache.serve_stale_on_failure_ttl", d.Cache.ServeStaleOnFailureTTL)

	v.SetDefault("admin.listen", d.Admin.Listen)

	v.SetDefault("logging.level", d.Logging.Level)
	v.SetDefault("logging.format", d.Logging.Format)
	v.SetDefault("logging.query_log.enabled", d.Logging.QueryLog.Enabled)

	v.SetDefault("shutdown_timeout", d.ShutdownTimeout)
	v.SetDefault("startup_probe_timeout", d.StartupProbeTimeout)
}
