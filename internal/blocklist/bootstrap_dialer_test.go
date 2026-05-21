package blocklist_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/stretchr/testify/require"
)

func TestBootstrapResolver_ResolvesViaConfiguredUpstream(t *testing.T) {
	addr := testutil.Spawn(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg)
		dnsutil.SetReply(resp, req)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.Header{Name: "fake.test.", Class: dns.ClassINET, TTL: 60},
			A:   rdata.A{Addr: netip.MustParseAddr("127.0.0.99")},
		}}
		return resp
	})

	res := blocklist.NewBootstrapResolver([]string{addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := res.LookupHost(ctx, "fake.test")
	require.NoError(t, err)
	require.Contains(t, ips, "127.0.0.99")
}
