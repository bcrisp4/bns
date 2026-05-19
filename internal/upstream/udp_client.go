package upstream

import (
	"context"
	"fmt"
	"net"
	"time"

	"codeberg.org/miekg/dns"
)

// UDPClient dials an upstream DNS server over UDP, with automatic
// retry-over-TCP if the UDP response has the TC (truncated) bit set.
// miekg/dns v2 does NOT auto-retry on TC; that is the caller's job.
//
// In v2 the network is selected by the address passed to Exchange, not a
// Client.Net field. Client configuration is through *Transport.
type UDPClient struct {
	Addr    string
	Timeout time.Duration

	client *dns.Client
}

// NewUDPClient constructs a UDPClient targeting addr ("host:port") with a
// per-exchange timeout applied to reads and writes.
func NewUDPClient(addr string, timeout time.Duration) *UDPClient {
	return &UDPClient{
		Addr:    addr,
		Timeout: timeout,
		client: &dns.Client{
			Transport: &dns.Transport{
				Dialer:       &net.Dialer{Timeout: timeout},
				ReadTimeout:  timeout,
				WriteTimeout: timeout,
			},
		},
	}
}

// Exchange sends req over UDP and returns the response. If the response is
// truncated (TC=1), it transparently retries the same request over TCP.
func (c *UDPClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	resp, _, err := c.client.Exchange(ctx, req, "udp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("udp exchange %s: %w", c.Addr, err)
	}
	if !resp.Truncated {
		return resp, nil
	}
	resp, _, err = c.client.Exchange(ctx, req, "tcp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("tcp retry %s: %w", c.Addr, err)
	}
	return resp, nil
}
