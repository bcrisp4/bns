// Package upstream tests live in-package (white-box) so we can inject
// custom RootCAs into the DoHClient's TLS config for httptest servers
// without exposing a production seam.
package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnshttp"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
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

// newTestDoHServer spins up an httptest TLS server with a self-signed
// cert covering 127.0.0.1, running the supplied handler. Returns a fully
// configured DoHClient pointing at it (test cert pool injected into the
// client's TLS config).
func newTestDoHServer(t *testing.T, handler http.HandlerFunc) *DoHClient {
	t.Helper()

	cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)

	dohURL := "https://" + parsed.Host + "/dns-query"
	c, err := NewDoHClient(
		dohURL,
		nil, // empty endpointIPs → DialContext falls back to URL host (127.0.0.1)
		5*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

	return c
}

// dohEchoHandler decodes the DoH request to a DNS message, hands it to
// build(), encodes the result and writes it back.
func dohEchoHandler(t *testing.T, build func(req *dns.Msg) *dns.Msg) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		msg, err := dnshttp.Request(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := build(msg)
		if err := resp.Pack(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
}

func TestDoHClient_RoundTrip(t *testing.T) {
	var seenIDOnWire uint16 = 99 // sentinel; should overwrite to 0
	handler := dohEchoHandler(t, func(req *dns.Msg) *dns.Msg {
		seenIDOnWire = req.ID
		resp := new(dns.Msg)
		resp.Response = true
		resp.Question = req.Question
		return resp
	})
	c := newTestDoHServer(t, handler)

	req := dns.NewMsg("example.com.", dns.TypeA)
	req.ID = 0x1234

	resp, err := c.Exchange(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.True(t, resp.Response)
	require.Equal(t, uint16(0x1234), resp.ID,
		"response ID must be restored to original")
	require.Equal(t, uint16(0x1234), req.ID,
		"caller's req.ID must be unchanged (immutability contract)")
	require.Equal(t, uint16(0), seenIDOnWire,
		"wire ID must be 0 (RFC 8484 §4.1.1)")
}

func TestDoHClient_POSTMethodAndHeaders(t *testing.T) {
	var seenMethod, seenCT, seenAccept string
	handler := func(w http.ResponseWriter, r *http.Request) {
		seenMethod = r.Method
		seenCT = r.Header.Get("Content-Type")
		seenAccept = r.Header.Get("Accept")
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)

	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
	require.Equal(t, "POST", seenMethod)
	require.Equal(t, "application/dns-message", seenCT)
	require.Equal(t, "application/dns-message", seenAccept)
}
