package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLoad_YAMLOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bns.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
listen:
  udp: ":5353"
upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
cache:
  capacity: 500
`), 0o644))

	cfg, err := config.Load(config.LoadOptions{
		ConfigPath: path,
	})
	require.NoError(t, err)
	require.Equal(t, ":5353", cfg.Listen.UDP)
	require.Equal(t, ":53", cfg.Listen.TCP) // default retained
	require.Equal(t, 500, cfg.Cache.Capacity)
	require.Len(t, cfg.Upstreams, 1)
	require.Equal(t, "1.1.1.1:53", cfg.Upstreams[0].Addr)
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bns.yaml")
	require.NoError(t, os.WriteFile(path, []byte("listen:\n  udp: \":5353\"\n"), 0o644))

	t.Setenv("BNS_LISTEN__UDP", ":6363")

	cfg, err := config.Load(config.LoadOptions{ConfigPath: path})
	require.NoError(t, err)
	require.Equal(t, ":6363", cfg.Listen.UDP)
}

func TestLoad_NoConfigFileOK(t *testing.T) {
	cfg, err := config.Load(config.LoadOptions{})
	require.NoError(t, err)
	require.Equal(t, ":53", cfg.Listen.UDP)
}
