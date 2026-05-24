// Package upstream — white-box integration test for the DoHClient + Pool path.
// Placed in-package so we can inject the test certificate into the DoHClient's
// TLS config without exposing a production seam.
package upstream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/netip"
	"testing"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnshttp"
	"codeberg.org/miekg/dns/rdata"

	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/stretchr/testify/require"
)

// TestIntegration_DoH_PoolEndToEnd wires a real DoHClient behind a real Pool
// and verifies the full path: Pool dispatches to DoHClient, DoH over TLS to
// the test server, response decoded and returned, and the per-query upstream
// marker captures the winning upstream's URL + protocol.
func TestIntegration_DoH_PoolEndToEnd(t *testing.T) {
	mtr := metrics.NewForTest()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := dnshttp.Request(r)
		require.NoError(t, err)

		qname := req.Question[0].Header().Name
		resp := new(dns.Msg)
		resp.Response = true
		resp.Question = req.Question
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.Header{Name: qname, TTL: 60},
			A:   rdata.A{Addr: netip.MustParseAddr("203.0.113.42")},
		}}
		require.NoError(t, resp.Pack())
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	})

	// newTestDoHServerWithMetrics spins up the httptest TLS server, injects
	// the self-signed cert into the client's RootCAs, and returns a ready
	// DoHClient (see doh_client_test.go).
	c := newTestDoHServerWithMetrics(t, handler, mtr)
	pool := NewPool([]Upstream{c}, mtr)

	ctx, _ := resolver.WithUpstreamMarker(context.Background())
	resp, err := pool.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Answer, 1)

	// Upstream marker must be set to the DoH URL and "doh" protocol.
	info, present := resolver.UpstreamInfoFrom(ctx)
	require.True(t, present)
	require.Equal(t, c.url, info.Name)
	require.Equal(t, "doh", info.Protocol)
}
