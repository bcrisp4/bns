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

func TestValidate_UDP_EmptyTypeDefaults(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
	}
	require.NoError(t, c.Validate())
}

func TestValidate_UDP_ExplicitType(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "udp", Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
	}
	require.NoError(t, c.Validate())
}

func TestValidate_UDP_RejectsDoHFields(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "udp", Addr: "1.1.1.1:53", URL: "https://x/dns-query", Timeout: 2 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "url/endpoint_ips not valid for type=udp")
}

func TestValidate_DoH_HostnameURL_OK(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{
			Type:        "doh",
			URL:         "https://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"1.1.1.1", "1.0.0.1"},
			Timeout:     5 * time.Second,
		},
	}
	require.NoError(t, c.Validate())
}

func TestValidate_DoH_MissingURL(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "doh", EndpointIPs: []string{"1.1.1.1"}, Timeout: 5 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "url is required for type=doh")
}

func TestValidate_DoH_NonHTTPSScheme(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "doh", URL: "http://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"1.1.1.1"}, Timeout: 5 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "scheme must be https")
}

func TestValidate_DoH_IPLiteralURL_Rejected(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "doh", URL: "https://1.1.1.1/dns-query", Timeout: 5 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "url host must be a hostname")
}

func TestValidate_DoH_MissingEndpointIPs(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "doh", URL: "https://cloudflare-dns.com/dns-query", Timeout: 5 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "endpoint_ips is required")
}

func TestValidate_DoH_InvalidEndpointIP(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{
			Type:        "doh",
			URL:         "https://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"not-an-ip"},
			Timeout:     5 * time.Second,
		},
	}
	require.ErrorContains(t, c.Validate(), "not a valid IP")
}

func TestValidate_DoH_AddrFieldRejected(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{
			Type:        "doh",
			Addr:        "1.1.1.1:53",
			URL:         "https://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"1.1.1.1"},
			Timeout:     5 * time.Second,
		},
	}
	require.ErrorContains(t, c.Validate(), "addr not valid for type=doh")
}

func TestValidate_UnknownType(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "doq", Addr: "1.1.1.1:853", Timeout: 5 * time.Second},
	}
	require.ErrorContains(t, c.Validate(), "must be \"udp\" or \"doh\"")
}

func TestValidate_HTTPBlocklist_RequiresBootstrapIPs(t *testing.T) {
	// DoH-only config with file-only blocklist source: no http source,
	// so no bootstrap needed → passes even though there are zero UDP
	// upstreams.
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{
			Type:        "doh",
			URL:         "https://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"1.1.1.1"},
			Timeout:     5 * time.Second,
		},
	}
	c.Blocklists.Sources = []config.BlocklistSource{
		{Type: "file", Name: "local", Path: "/dev/null"},
	}
	require.NoError(t, c.Validate())
}

func TestValidate_HTTPBlocklist_WithUDPUpstream_OK(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{Type: "udp", Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
	}
	c.Blocklists.Sources = []config.BlocklistSource{
		{Type: "http", Name: "hagezi-pro", URL: "https://x/y"},
	}
	require.NoError(t, c.Validate())
}

func TestValidate_HTTPBlocklist_WithDoHEndpointIPs_OK(t *testing.T) {
	c := validatableConfig()
	c.Upstreams = []config.Upstream{
		{
			Type:        "doh",
			URL:         "https://cloudflare-dns.com/dns-query",
			EndpointIPs: []string{"1.1.1.1"},
			Timeout:     5 * time.Second,
		},
	}
	c.Blocklists.Sources = []config.BlocklistSource{
		{Type: "http", Name: "hagezi-pro", URL: "https://x/y"},
	}
	// DoH endpoint_ips serve as bootstrap-IP source (paired with :53).
	require.NoError(t, c.Validate())
}
