package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// loadWithArgs builds a fresh serve command, parses the given args, and
// returns the Config produced via config.Load with bindServeFlags as the
// FlagBinder hook. Mirrors the production wiring from newServeCmd's RunE.
func loadWithArgs(t *testing.T, cfgPath string, args ...string) config.Config {
	t.Helper()
	cmd := newServeCmd()
	require.NoError(t, cmd.ParseFlags(args))
	cfg, err := config.Load(config.LoadOptions{
		ConfigPath: cfgPath,
		FlagBinder: func(v *viper.Viper) error { return bindServeFlags(v, cmd) },
	})
	require.NoError(t, err)
	return cfg
}

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bns.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// Without --blocklist the YAML sources survive.
func TestServeFlags_BlocklistFromYAML(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
blocklists:
  sources:
    - type: file
      name: yaml-list
      path: /tmp/from-yaml.txt
`)
	cfg := loadWithArgs(t, yaml)
	require.Len(t, cfg.Blocklists.Sources, 1)
	require.Equal(t, "/tmp/from-yaml.txt", cfg.Blocklists.Sources[0].Path)
}

// With --blocklist the YAML sources are fully replaced.
func TestServeFlags_BlocklistReplacesYAML(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
blocklists:
  sources:
    - type: file
      name: yaml-list
      path: /tmp/from-yaml.txt
`)
	cfg := loadWithArgs(t, yaml,
		"--blocklist", "/tmp/a.txt",
		"--blocklist", "/tmp/b.txt",
	)
	require.Len(t, cfg.Blocklists.Sources, 2)
	require.Equal(t, "file", cfg.Blocklists.Sources[0].Type)
	require.Equal(t, "/tmp/a.txt", cfg.Blocklists.Sources[0].Path)
	require.Equal(t, "/tmp/b.txt", cfg.Blocklists.Sources[1].Path)
}

// --upstream and --blocklist may be combined; both replace their YAML counterparts.
func TestServeFlags_UpstreamAndBlocklistTogether(t *testing.T) {
	cfg := loadWithArgs(t, "",
		"--upstream", "9.9.9.9:53",
		"--blocklist", "/tmp/x.txt",
	)
	require.Len(t, cfg.Upstreams, 1)
	require.Equal(t, "9.9.9.9:53", cfg.Upstreams[0].Addr)
	require.Equal(t, 2*time.Second, cfg.Upstreams[0].Timeout)
	require.Len(t, cfg.Blocklists.Sources, 1)
	require.Equal(t, "/tmp/x.txt", cfg.Blocklists.Sources[0].Path)
}

// Duration flag (--cache.max-ttl) overrides YAML.
func TestServeFlags_DurationOverride(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
cache:
  max_ttl: 1h
`)
	cfg := loadWithArgs(t, yaml, "--cache.max-ttl", "30m")
	require.Equal(t, 30*time.Minute, cfg.Cache.MaxTTL)
}

// Unset Duration flag falls through to YAML (proves viper.HasChanged gate works).
func TestServeFlags_UnsetDurationLeavesYAML(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
cache:
  max_ttl: 1h
`)
	cfg := loadWithArgs(t, yaml)
	require.Equal(t, 1*time.Hour, cfg.Cache.MaxTTL)
}

// Int flag (--cache.capacity) overrides YAML.
func TestServeFlags_IntOverride(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
cache:
  capacity: 500
`)
	cfg := loadWithArgs(t, yaml, "--cache.capacity", "9000")
	require.Equal(t, 9000, cfg.Cache.Capacity)
}

// Bool flag (--logging.query-log) overrides YAML.
func TestServeFlags_BoolOverride(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
logging:
  query_log:
    enabled: false
`)
	cfg := loadWithArgs(t, yaml, "--logging.query-log")
	require.True(t, cfg.Logging.QueryLog.Enabled)
}

// Unset Bool flag falls through to YAML — bool is the trickiest case for
// BindPFlag because the pflag value is always non-empty.
func TestServeFlags_UnsetBoolLeavesYAML(t *testing.T) {
	yaml := writeYAML(t, `
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
logging:
  query_log:
    enabled: true
`)
	cfg := loadWithArgs(t, yaml)
	require.True(t, cfg.Logging.QueryLog.Enabled)
}

// String flag (--logging.level) wins over env (flag > env precedence).
func TestServeFlags_FlagOverridesEnv(t *testing.T) {
	t.Setenv("BNS_LOGGING__LEVEL", "error")
	cfg := loadWithArgs(t, "",
		"--upstream", "1.1.1.1:53",
		"--logging.level", "debug",
	)
	require.Equal(t, "debug", cfg.Logging.Level)
}
