package upstream_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/stretchr/testify/require"
)

func TestFactory_UDP_Default(t *testing.T) {
	cfg := config.Upstream{
		Type:    "", // defaults to udp
		Addr:    "1.1.1.1:53",
		Timeout: 2 * time.Second,
	}
	u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
	require.NoError(t, err)
	require.Equal(t, "udp", u.Protocol())
	require.Equal(t, "1.1.1.1:53", u.Name())
}

func TestFactory_UDP_Explicit(t *testing.T) {
	cfg := config.Upstream{
		Type:    "udp",
		Addr:    "8.8.8.8:53",
		Timeout: 2 * time.Second,
	}
	u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
	require.NoError(t, err)
	require.Equal(t, "8.8.8.8:53", u.Name())
}

func TestFactory_DoH(t *testing.T) {
	cfg := config.Upstream{
		Type:        "doh",
		URL:         "https://cloudflare-dns.com/dns-query",
		EndpointIPs: []string{"1.1.1.1"},
		Timeout:     5 * time.Second,
	}
	u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
	require.NoError(t, err)
	require.Equal(t, "doh", u.Protocol())
	require.Equal(t, "https://cloudflare-dns.com/dns-query", u.Name())
}

func TestFactory_UnknownType(t *testing.T) {
	cfg := config.Upstream{Type: "doq", Timeout: 2 * time.Second}
	_, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
	require.ErrorContains(t, err, "unknown type")
}
