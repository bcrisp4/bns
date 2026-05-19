package upstream_test

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/stretchr/testify/require"
)

// newAReply builds a minimal A-record reply for req.
func newAReply(req *dns.Msg, ip string) *dns.Msg {
	resp := new(dns.Msg)
	dnsutil.SetReply(resp, req)
	rr := &dns.A{
		Hdr: dns.Header{Name: "example.com.", Class: dns.ClassINET, TTL: 60},
		A:   rdata.A{Addr: netip.MustParseAddr(ip)},
	}
	resp.Answer = []dns.RR{rr}
	return resp
}

func TestUDPClient_Success(t *testing.T) {
	addr := testutil.Spawn(t, func(req *dns.Msg) *dns.Msg {
		return newAReply(req, "1.2.3.4")
	})

	c := upstream.NewUDPClient(addr, 2*time.Second)
	req := dns.NewMsg("example.com.", dns.TypeA)

	resp, err := c.Exchange(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Answer, 1)
}

func TestUDPClient_TruncationRetriesOverTCP(t *testing.T) {
	var sawTCP atomic.Bool
	addr := testutil.Spawn(t, func(req *dns.Msg) *dns.Msg {
		resp := newAReply(req, "1.2.3.4")
		// Mark TC on the first request (UDP); answer fully on the retry (TCP).
		if !sawTCP.Load() {
			resp.Truncated = true
			sawTCP.Store(true)
		}
		return resp
	})

	c := upstream.NewUDPClient(addr, 2*time.Second)
	req := dns.NewMsg("example.com.", dns.TypeA)

	resp, err := c.Exchange(context.Background(), req)
	require.NoError(t, err)
	require.False(t, resp.Truncated)
	require.True(t, sawTCP.Load(), "client should have retried over TCP")
}
