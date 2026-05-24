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
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnshttp"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	ptestutil "github.com/prometheus/client_golang/prometheus/testutil"
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

// newTestDoHServerWithMetrics spins up an httptest TLS server with a
// self-signed cert covering 127.0.0.1, running the supplied handler.
// Returns a fully configured DoHClient pointing at it (test cert pool
// injected into the client's TLS config), using the supplied *metrics.Metrics
// so callers can assert counter values via prometheus/testutil.
func newTestDoHServerWithMetrics(t *testing.T, handler http.HandlerFunc, mtr *metrics.Metrics) *DoHClient {
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
		mtr,
	)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

	return c
}

// newTestDoHServer is a convenience wrapper around newTestDoHServerWithMetrics
// for tests that don't need to assert metric counter values.
func newTestDoHServer(t *testing.T, handler http.HandlerFunc) *DoHClient {
	t.Helper()
	return newTestDoHServerWithMetrics(t, handler, metrics.NewForTest())
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

func TestDoHClient_HTTPNon2xx(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.ErrorContains(t, err, "http 500")
}

func TestDoHClient_Accepts2xxRange(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// 202 Accepted — uncommon but spec-compliant 2xx.
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
}

func TestDoHClient_ContentTypeWithCharsetParameter(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message; charset=utf-8")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err, "charset parameter must not reject valid DoH body")
}

func TestDoHClient_ContentTypeMismatch(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not dns"))
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.ErrorContains(t, err, "unexpected content-type")
}

func TestDoHClient_RefusesRedirect(t *testing.T) {
	// Second server should never be hit.
	var secondHits int32
	secondCert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
	second := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		atomic.AddInt32(&secondHits, 1)
	}))
	second.TLS = &tls.Config{Certificates: []tls.Certificate{secondCert}}
	second.StartTLS()
	t.Cleanup(second.Close)

	handler := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/dns-query", http.StatusFound)
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.ErrorContains(t, err, "http 302")
	require.Equal(t, int32(0), atomic.LoadInt32(&secondHits),
		"redirect target must not be followed")
}

func TestDoHClient_BodyCapPreventsOverflow(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dns-message")
		// Write more than dns.MaxMsgSize (65535) bytes of garbage; the
		// client's LimitReader caps reads at 65535 → decode fails cleanly.
		buf := make([]byte, dns.MaxMsgSize+1024)
		_, _ = w.Write(buf)
	}
	c := newTestDoHServer(t, handler)
	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.Error(t, err, "oversized body must fail decode, not OOM")
}

func TestDoHClient_CookieJarIsNil(t *testing.T) {
	c, err := NewDoHClient(
		"https://x/dns-query",
		[]string{"1.1.1.1"},
		5*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.NoError(t, err)
	require.Nil(t, c.httpClient.Jar)
}

// buildAnswerMsg returns a DoH-style response with one A record at TTL.
func buildAnswerMsg(req *dns.Msg, ttl uint32) *dns.Msg {
	resp := new(dns.Msg)
	resp.Response = true
	resp.Question = req.Question
	a := &dns.A{
		Hdr: dns.Header{Name: "example.com.", TTL: ttl},
		A:   rdata.A{Addr: netip.MustParseAddr("203.0.113.1")},
	}
	resp.Answer = []dns.RR{a}
	return resp
}

func TestDoHClient_AgeHeaderDecrementsTTL(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		req, err := dnshttp.Request(r)
		require.NoError(t, err)
		resp := buildAnswerMsg(req, 300)
		require.NoError(t, resp.Pack())
		w.Header().Set("Content-Type", "application/dns-message")
		w.Header().Set("Age", "60")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)

	resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
	require.Len(t, resp.Answer, 1)
	require.Equal(t, uint32(240), resp.Answer[0].Header().TTL)
}

func TestDoHClient_NoAgeHeader_TTLUnchanged(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		req, err := dnshttp.Request(r)
		require.NoError(t, err)
		resp := buildAnswerMsg(req, 300)
		require.NoError(t, resp.Pack())
		w.Header().Set("Content-Type", "application/dns-message")
		// No Age header.
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)

	resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
	require.Equal(t, uint32(300), resp.Answer[0].Header().TTL)
}

func TestDoHClient_AgeExceedsTTL_FloorsAtZero(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		req, err := dnshttp.Request(r)
		require.NoError(t, err)
		resp := buildAnswerMsg(req, 30)
		require.NoError(t, resp.Pack())
		w.Header().Set("Content-Type", "application/dns-message")
		w.Header().Set("Age", "120")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServer(t, handler)

	resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp.Answer[0].Header().TTL)
}

func TestDoHClient_ContextCancellation(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Block until client disconnects.
		<-r.Context().Done()
	}
	c := newTestDoHServer(t, handler)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_, err := c.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// TestDoHClient_PackError_RestoresOriginalID verifies that the deferred ID
// restore in Exchange runs even when req.Pack() fails, preserving the caller's
// req.ID on every return path.
//
// Pack fails when a DNS label is >= 64 bytes (miekg/dns v2 internal/pack/pack.go:
// "labelLen >= 1<<6" returns "illegal label type in name"). A 64-char label is a
// reliable, version-stable trigger.
func TestDoHClient_PackError_RestoresOriginalID(t *testing.T) {
	// Construct a Msg whose question name has a 64-char label — Pack rejects it
	// with "illegal label type in name" before any network I/O.
	label64 := strings.Repeat("a", 64)
	req := dns.NewMsg(label64+".example.com.", dns.TypeA)
	req.ID = 0xBEEF

	c, err := NewDoHClient(
		"https://x/dns-query",
		[]string{"127.0.0.1"},
		5*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.NoError(t, err)

	_, exErr := c.Exchange(context.Background(), req)
	require.Error(t, exErr)
	require.Equal(t, uint16(0xBEEF), req.ID,
		"caller's req.ID must be restored even on Pack failure")
}

func TestDoHClient_DialFailover(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}

	cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(handler))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	parsed, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(parsed.Host)

	// dohURL host is the test server's host:port pair so the URL parser
	// gives us the correct port to dial on the unreachable IP as well.
	dohURL := "https://127.0.0.1:" + port + "/dns-query"

	// Endpoint IPs: first one is unreachable (127.0.0.99 — nothing
	// listens on the ephemeral test port there); second is the actual
	// server.
	c, err := NewDoHClient(
		dohURL,
		[]string{"127.0.0.99", "127.0.0.1"},
		2*time.Second,
		slog.New(slog.DiscardHandler),
		metrics.NewForTest(),
	)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)
	c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

	_, err = c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err, "dial should have failed over from 127.0.0.99 to 127.0.0.1")
}

func TestDoHClient_RecordsHTTPStatusMetric(t *testing.T) {
	mtr := metrics.NewForTest()
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServerWithMetrics(t, handler, mtr)

	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)

	count := ptestutil.ToFloat64(mtr.DoHHTTPStatusTotal.WithLabelValues(c.url, "200"))
	require.Equal(t, float64(1), count)
}

func TestDoHClient_RecordsTLSHandshakeMetric(t *testing.T) {
	mtr := metrics.NewForTest()
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := new(dns.Msg)
		resp.Response = true
		_ = resp.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(resp.Data))
	}
	c := newTestDoHServerWithMetrics(t, handler, mtr)

	_, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)

	count := ptestutil.ToFloat64(mtr.DoHTLSHandshakesTotal.WithLabelValues(c.url, "ok"))
	require.GreaterOrEqual(t, count, float64(1))
}
