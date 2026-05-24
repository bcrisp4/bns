// Package upstream tests live in-package (white-box) so we can inject
// custom RootCAs into the DoHClient's TLS config for httptest servers
// without exposing a production seam.
package upstream

import (
	"log/slog"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestDoHClient_NameAndProtocol(t *testing.T) {
	c, err := NewDoHClient(
		"https://cloudflare-dns.com/dns-query",
		[]string{"1.1.1.1"},
		5*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.NoError(t, err)
	require.Equal(t, "https://cloudflare-dns.com/dns-query", c.Name())
	require.Equal(t, "doh", c.Protocol())
}

func TestDoHClient_InvalidURL(t *testing.T) {
	_, err := NewDoHClient(
		"://broken",
		[]string{"1.1.1.1"},
		5*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.Error(t, err)
}
