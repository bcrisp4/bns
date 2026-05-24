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
//
// Type selects the transport: "udp" (default) or "doh". For type=udp,
// Addr is required (host:port form). For type=doh, URL is required
// (https://hostname/path form) and EndpointIPs must be non-empty
// (operator-pinned IPs that DialContext substitutes for the URL host;
// hostname is retained for SNI / cert validation).
type Upstream struct {
	Type        string        `mapstructure:"type"`         // "udp" | "doh"; default "udp"
	Addr        string        `mapstructure:"addr"`         // type=udp only
	URL         string        `mapstructure:"url"`          // type=doh only
	EndpointIPs []string      `mapstructure:"endpoint_ips"` // type=doh only; required, non-empty
	Timeout     time.Duration `mapstructure:"timeout"`
}

// Cache configures the in-memory LRU cache.
type Cache struct {
	Capacity       int           `mapstructure:"capacity"`
	MinTTL         time.Duration `mapstructure:"min_ttl"`
	MaxTTL         time.Duration `mapstructure:"max_ttl"`
	NegativeTTLMax time.Duration `mapstructure:"negative_ttl_max"`
}

// Blocklists configures the ad-blocking blocklist sources and the
// background HTTP fetcher.
type Blocklists struct {
	// RefreshInterval is the global cadence at which HTTP sources are
	// re-fetched. Must be >= 1m when set. Zero disables auto-refresh
	// (sources still reload via SIGHUP).
	RefreshInterval time.Duration `mapstructure:"refresh_interval"`
	// CacheDir is the directory used to persist fetched HTTP source
	// bodies and their sidecar metadata. Created on first write.
	CacheDir string `mapstructure:"cache_dir"`
	// Sources are the configured blocklist sources, in order.
	Sources []BlocklistSource `mapstructure:"sources"`
}

// BlocklistSource is one source of blocklist entries.
//
// Name is required and used as the metric label and log field. It must
// be unique across sources.
type BlocklistSource struct {
	Type string `mapstructure:"type"` // "file" | "http"
	Name string `mapstructure:"name"`
	Path string `mapstructure:"path"` // for type="file"
	URL  string `mapstructure:"url"`  // for type="http"
}

// Admin configures the HTTP server hosting /metrics, /healthz, /readyz.
type Admin struct {
	Listen string `mapstructure:"listen"`
	Pprof  bool   `mapstructure:"pprof"`
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
