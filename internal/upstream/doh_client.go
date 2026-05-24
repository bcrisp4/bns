// Package upstream — DoHClient implements DNS-over-HTTPS (RFC 8484) as an
// Upstream. URL host is operator-meaningful (used as SNI + cert SAN check);
// endpointIPs are the actual dial targets. Hand-rolled over stdlib net/http;
// response decode delegates to miekg/dns v2's dnshttp.Response helper.
package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnshttp"
	"github.com/bcrisp4/bns/internal/metrics"
	"golang.org/x/net/http2"
)

const dohContentType = "application/dns-message"

// DoHClient sends DNS queries over HTTPS to a configured DoH endpoint.
type DoHClient struct {
	url        string
	httpClient *http.Client
	logger     *slog.Logger
	metrics    *metrics.Metrics
}

// NewDoHClient constructs a DoHClient. rawURL must be an https URL whose
// hostname matches a SAN in the server's certificate. endpointIPs are
// dialed in round-robin order; net/http performs the TLS handshake using
// rawURL's hostname as SNI. timeout applies to the whole HTTP exchange.
//
// logger must be non-nil; pass slog.New(slog.DiscardHandler) for tests
// that don't care. mtr must be non-nil; pass metrics.NewForTest().
func NewDoHClient(
	rawURL string,
	endpointIPs []string,
	timeout time.Duration,
	logger *slog.Logger,
	mtr *metrics.Metrics,
) (*DoHClient, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("doh url %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("doh url %q: missing host", rawURL)
	}

	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}

	// dialAddrs: explicit endpoint IPs, else fall back to URL host (which
	// is then expected to be an IP literal — callers from validate.go
	// never hit this case for production DoH, but it's relied on for
	// tests that pass an IP-literal URL with no endpointIPs).
	dialAddrs := append([]string(nil), endpointIPs...)
	if len(dialAddrs) == 0 {
		dialAddrs = []string{host}
	}

	var rr atomic.Uint32
	netDialer := &net.Dialer{Timeout: timeout}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			ServerName: host,
			NextProtos: []string{"h2", "http/1.1"},
			MinVersion: tls.VersionTLS13, // BCP 195 / RFC 9325
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			start := int(rr.Add(1)-1) % len(dialAddrs)
			var lastErr error
			for i := 0; i < len(dialAddrs); i++ {
				ip := dialAddrs[(start+i)%len(dialAddrs)]
				c, derr := netDialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if derr == nil {
					return c, nil
				}
				lastErr = derr
			}
			return nil, lastErr
		},
		ForceAttemptHTTP2:   true,
		IdleConnTimeout:     90 * time.Second,
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
	}
	if err := http2.ConfigureTransport(transport); err != nil {
		return nil, fmt.Errorf("doh %s: configure http/2: %w", rawURL, err)
	}

	return &DoHClient{
		url:     rawURL,
		logger:  logger,
		metrics: mtr,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// RFC 8484 §9 spirit: a hijacked DoH server should not redirect
			// the client to a logging or alternate endpoint.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			// RFC 8484 §8: cookies SHOULD NOT be accepted. Jar nil by default;
			// explicit for clarity and to make a future test assertion simple.
			Jar: nil,
		},
	}, nil
}

// Name returns the configured DoH URL (used as the "upstream" metric label).
func (c *DoHClient) Name() string { return c.url }

// Protocol returns "doh".
func (c *DoHClient) Protocol() string { return "doh" }

// Exchange sends req to the DoH endpoint and returns the decoded response.
//
// RFC 8484 §4.1.1: the DNS message ID is set to 0 on the wire so HTTP caches
// don't fragment on per-query IDs. The caller's original ID is saved via defer
// and restored on every return path, preserving the Upstream "MUST NOT mutate
// req" contract.
func (c *DoHClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	// RFC 8484 §4.1.1: DNS ID SHOULD be 0 on the wire so HTTP caches
	// don't fragment on per-query IDs. Save+restore the caller's ID via
	// defer so the Upstream "MUST NOT mutate req" contract holds on
	// every return path.
	origID := req.ID
	req.ID = 0
	defer func() { req.ID = origID }()

	if err := req.Pack(); err != nil {
		return nil, fmt.Errorf("doh %s: pack: %w", c.url, err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.url, bytes.NewReader(req.Data),
	)
	if err != nil {
		return nil, fmt.Errorf("doh %s: build request: %w", c.url, err)
	}
	httpReq.Header.Set("Content-Type", dohContentType)
	httpReq.Header.Set("Accept", dohContentType)
	httpReq.Header.Set("User-Agent", "bns")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("doh %s: do: %w", c.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("doh %s: http %d", c.url, resp.StatusCode)
	}

	// RFC 8484 §4.2 + RFC 7231 §3.1.1.1: case-insensitive type token,
	// parameters (e.g. charset=utf-8) permitted.
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, dohContentType) {
		return nil, fmt.Errorf("doh %s: unexpected content-type %q",
			c.url, resp.Header.Get("Content-Type"))
	}

	// RFC 8484 §6 + DoS defense: cap body at the DNS msg size ceiling.
	resp.Body = io.NopCloser(io.LimitReader(resp.Body, dns.MaxMsgSize))

	msg, err := dnshttp.Response(resp)
	if err != nil {
		return nil, fmt.Errorf("doh %s: decode: %w", c.url, err)
	}

	// RFC 8484 §5.1: subtract HTTP Age (HTTP intermediate cache hold time)
	// from RR TTLs so downstream caching reflects true remaining freshness.
	// See decrementTTLs for full explanation.
	if ageStr := resp.Header.Get("Age"); ageStr != "" {
		if age, parseErr := strconv.ParseUint(ageStr, 10, 32); parseErr == nil && age > 0 {
			decrementTTLs(msg, uint32(age))
		}
	}

	msg.ID = origID // restore so downstream stages + wire writer match req
	return msg, nil
}

// decrementTTLs subtracts age (seconds) from every Answer/Ns/Extra RR
// TTL in m, flooring at 0.
//
// Why this matters (RFC 8484 §5.1):
// HTTP Age (RFC 7234 §5.1) reports how long an HTTP intermediate cache
// has held the response since the origin generated it. DNS RR TTLs in
// the wire body reflect remaining-TTL when the response reached the
// intermediate, NOT when it reached BNS. Without this adjustment,
// downstream caching overstates remaining freshness by Age seconds and
// serves stale answers.
//
// Worked example: authoritative TTL=300s.
//
//	T=0    Auth emits msg{TTL=300}.
//	T=60   Origin DoH server emits msg{TTL=240} (decremented its 60s hold).
//	T=90   Reaches HTTP intermediate cache.
//	T=120  BNS queries → CDN returns body{TTL=240} + Age:30.
//
// Without subtraction: BNS caches TTL=240, expires at T=360 (60s late).
// With subtraction:    BNS caches TTL=210, expires at T=330 (correct).
//
// Direct connections to public DoH providers usually have no intermediate
// cache → Age absent → this is a no-op. Defensive correctness for the
// CDN-fronted case at ~6 LOC.
func decrementTTLs(m *dns.Msg, age uint32) {
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			if rr.Header().TTL > age {
				rr.Header().TTL -= age
			} else {
				rr.Header().TTL = 0
			}
		}
	}
}
