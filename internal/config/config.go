// Package config defines the BNS configuration schema and loader.
//
// Precedence (highest first): CLI flags > environment variables > YAML file > defaults.
package config

import "time"

// Config is the top-level configuration struct, parsed once at startup.
type Config struct {
	Listen              Listen        `mapstructure:"listen"`
	Upstreams           []Upstream    `mapstructure:"upstreams"`
	Cache               Cache         `mapstructure:"cache"`
	Blocklists          Blocklists    `mapstructure:"blocklists"`
	Admin               Admin         `mapstructure:"admin"`
	Logging             Logging       `mapstructure:"logging"`
	ShutdownTimeout     time.Duration `mapstructure:"shutdown_timeout"`
	StartupProbeTimeout time.Duration `mapstructure:"startup_probe_timeout"`
}

// Listen configures the DNS listeners.
type Listen struct {
	UDP          string        `mapstructure:"udp"`
	TCP          string        `mapstructure:"tcp"`
	QueryTimeout time.Duration `mapstructure:"query_timeout"`
}

// Upstream is one configured upstream DNS server.
type Upstream struct {
	Addr    string        `mapstructure:"addr"`
	Timeout time.Duration `mapstructure:"timeout"`
}

// Cache configures the in-memory LRU cache.
type Cache struct {
	Capacity       int           `mapstructure:"capacity"`
	MinTTL         time.Duration `mapstructure:"min_ttl"`
	MaxTTL         time.Duration `mapstructure:"max_ttl"`
	NegativeTTLMax time.Duration `mapstructure:"negative_ttl_max"`
}

// Blocklists configures the ad-blocking blocklist sources.
type Blocklists struct {
	Sources []BlocklistSource `mapstructure:"sources"`
}

// BlocklistSource is one source of blocklist entries.
type BlocklistSource struct {
	Type string `mapstructure:"type"` // "file"
	Path string `mapstructure:"path"` // for type="file"
}

// Admin configures the HTTP server hosting /metrics, /healthz, /readyz.
type Admin struct {
	Listen string `mapstructure:"listen"`
}

// Logging configures structured logging and query logging.
type Logging struct {
	Level    string   `mapstructure:"level"`  // "debug" | "info" | "warn" | "error"
	Format   string   `mapstructure:"format"` // "json" | "text"
	QueryLog QueryLog `mapstructure:"query_log"`
}

// QueryLog gates the per-query log stream.
type QueryLog struct {
	Enabled bool `mapstructure:"enabled"`
}
