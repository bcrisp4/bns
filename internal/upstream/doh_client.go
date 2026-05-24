// Package upstream — DoHClient implements DNS-over-HTTPS (RFC 8484) as an
// Upstream. URL host is operator-meaningful (used as SNI + cert SAN check);
// endpointIPs are the actual dial targets. Hand-rolled over stdlib net/http;
// response decode delegates to miekg/dns v2's dnshttp.Response helper.
package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
	"golang.org/x/net/http2"
)

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

// Exchange is implemented in a later commit.
func (c *DoHClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	return nil, fmt.Errorf("doh %s: Exchange not yet implemented", c.url)
}
