package config_test

import (
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/stretchr/testify/require"
)

func TestValidate_RequiresUpstream(t *testing.T) {
	cfg := config.Default()
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "upstream")
}

func TestValidate_RejectsBadUDPBind(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: time.Second}}
	cfg.Listen.UDP = "not-a-host:port"
	err := cfg.Validate()
	require.Error(t, err)
}

func TestValidate_OK(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: 2 * time.Second}}
	require.NoError(t, cfg.Validate())
}

func TestValidate_RejectsBadLogLevel(t *testing.T) {
	cfg := config.Default()
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: time.Second}}
	cfg.Logging.Level = "verbose"
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "level")
}
