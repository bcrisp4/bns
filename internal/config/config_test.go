package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDefaultsFillEverything(t *testing.T) {
	cfg := config.Default()

	require.Equal(t, ":53", cfg.Listen.UDP)
	require.Equal(t, ":53", cfg.Listen.TCP)
	require.Equal(t, 5*time.Second, cfg.Listen.QueryTimeout)

	require.Equal(t, 10000, cfg.Cache.Capacity)
	require.Equal(t, 24*time.Hour, cfg.Cache.MaxTTL)
	require.Equal(t, 15*time.Minute, cfg.Cache.NegativeTTLMax)

	require.Equal(t, ":9090", cfg.Admin.Listen)
	require.Equal(t, "info", cfg.Logging.Level)
	require.Equal(t, "json", cfg.Logging.Format)
	require.False(t, cfg.Logging.QueryLog.Enabled)
	require.Equal(t, 5*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, 3*time.Second, cfg.StartupProbeTimeout)

	require.Nil(t, cfg.Upstreams)
	require.Empty(t, cfg.Blocklists.Sources)
	require.Equal(t, time.Duration(0), cfg.Cache.MinTTL)
}

func TestBlocklistsSchema_AcceptsHTTPSourceAndGlobalKeys(t *testing.T) {
	yaml := []byte(`
listen: {udp: ":53", tcp: ":53", query_timeout: 5s}
upstreams:
  - {addr: "1.1.1.1:53", timeout: 2s}
cache: {capacity: 1000, min_ttl: 0s, max_ttl: 1h, negative_ttl_max: 5m}
blocklists:
  refresh_interval: 12h
  cache_dir: /tmp/bns-cache
  sources:
    - {type: file, name: custom, path: /etc/custom.txt}
    - {type: http, name: hagezi-pro, url: "https://example.com/pro.txt"}
admin: {listen: ":9090"}
logging: {level: info, format: json}
shutdown_timeout: 5s
startup_probe_timeout: 3s
`)
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, yaml, 0o644))

	cfg, err := config.Load(config.LoadOptions{ConfigPath: path})
	require.NoError(t, err)
	require.Equal(t, 12*time.Hour, cfg.Blocklists.RefreshInterval)
	require.Equal(t, "/tmp/bns-cache", cfg.Blocklists.CacheDir)
	require.Len(t, cfg.Blocklists.Sources, 2)
	require.Equal(t, "file", cfg.Blocklists.Sources[0].Type)
	require.Equal(t, "custom", cfg.Blocklists.Sources[0].Name)
	require.Equal(t, "/etc/custom.txt", cfg.Blocklists.Sources[0].Path)
	require.Equal(t, "http", cfg.Blocklists.Sources[1].Type)
	require.Equal(t, "hagezi-pro", cfg.Blocklists.Sources[1].Name)
	require.Equal(t, "https://example.com/pro.txt", cfg.Blocklists.Sources[1].URL)
}
