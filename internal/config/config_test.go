package config_test

import (
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
	require.Equal(t, 5*time.Second, cfg.Cache.ServeStaleOnFailureTTL)

	require.Equal(t, ":9090", cfg.Admin.Listen)
	require.Equal(t, "info", cfg.Logging.Level)
	require.Equal(t, "json", cfg.Logging.Format)
	require.False(t, cfg.Logging.QueryLog.Enabled)
	require.Equal(t, 5*time.Second, cfg.ShutdownTimeout)
	require.Equal(t, 3*time.Second, cfg.StartupProbeTimeout)
}
