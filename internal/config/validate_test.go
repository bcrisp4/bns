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

// validatableConfig returns a config that passes Validate() apart from
// any tweaks the caller applies.
func validatableConfig() config.Config {
	cfg := config.Default()
	cfg.Listen = config.Listen{UDP: ":53", TCP: ":53", QueryTimeout: time.Second}
	cfg.Admin = config.Admin{Listen: ":9090"}
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: time.Second}}
	return cfg
}

func TestValidate_BlocklistSource_NameRequired(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "file", Path: "/x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "name")
}

func TestValidate_BlocklistSource_NameUnique(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{
		{Type: "file", Name: "dup", Path: "/a"},
		{Type: "file", Name: "dup", Path: "/b"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unique")
}

func TestValidate_BlocklistSource_HTTPRequiresURL(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "http", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "url is required")
}

func TestValidate_BlocklistSource_HTTPURLSchemeRejected(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "http", Name: "x", URL: "ftp://example.com/x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")
}

func TestValidate_BlocklistSource_FileRequiresPath(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "file", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "path is required")
}

func TestValidate_BlocklistSource_TypeAllowlist(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "ftp", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "type")
}

func TestValidate_RefreshInterval_MinFloor(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.RefreshInterval = 30 * time.Second
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "refresh_interval")
}
