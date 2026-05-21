package config

import "time"

// Default returns a Config populated with the documented MVP defaults.
// See docs/specs/2026-05-19-bns-mvp-design.md §7 for the canonical schema.
func Default() Config {
	return Config{
		Listen: Listen{
			UDP:          ":53",
			TCP:          ":53",
			QueryTimeout: 5 * time.Second,
		},
		Upstreams: nil,
		Cache: Cache{
			Capacity:       10000,
			MinTTL:         0,
			MaxTTL:         24 * time.Hour,
			NegativeTTLMax: 15 * time.Minute,
		},
		Blocklists: Blocklists{
			RefreshInterval: 24 * time.Hour,
			CacheDir:        "/var/cache/bns/blocklists",
		},
		Admin: Admin{Listen: ":9090"},
		Logging: Logging{
			Level:    "info",
			Format:   "json",
			QueryLog: QueryLog{Enabled: false},
		},
		ShutdownTimeout:     5 * time.Second,
		StartupProbeTimeout: 3 * time.Second,
	}
}
