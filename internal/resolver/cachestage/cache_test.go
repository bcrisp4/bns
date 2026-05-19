package cachestage_test

import (
	"context"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/cachestage"
	"github.com/stretchr/testify/require"
)

func defaults() config.Cache {
	return config.Cache{
		Capacity:       100,
		MinTTL:         0,
		MaxTTL:         24 * time.Hour,
		NegativeTTLMax: 15 * time.Minute,
	}
}

// answer builds a positive DNS response with a single A record.
// name must be fully qualified (trailing dot).
func answer(name string, ttl uint32, ip string) *dns.Msg {
	m := new(dns.Msg)
	m.Response = true
	// Question section: bare A RR with only header fields set (v2 pattern).
	q := &dns.A{Hdr: dns.Header{Name: name, Class: dns.ClassINET}}
	m.Question = []dns.RR{q}
	// Answer section: full A RR with TTL and address.
	a := &dns.A{
		Hdr: dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
		A:   rdata.A{Addr: netip.MustParseAddr(ip)},
	}
	m.Answer = []dns.RR{a}
	return m
}

func TestCacheStage_MissThenHit(t *testing.T) {
	var calls atomic.Int64
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		calls.Add(1)
		return answer(req.Question[0].Header().Name, 300, "1.2.3.4"), nil
	})

	r := cachestage.New(next, cache.NewLRU(10), defaults())

	req := dns.NewMsg("example.com.", dns.TypeA)

	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Resolve(context.Background(), req)
	require.NoError(t, err)

	require.EqualValues(t, 1, calls.Load(), "second call should be a cache hit")
}

func TestCacheStage_NXDomainNegativeCachedWithSOAMin(t *testing.T) {
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.Response = true
		m.ID = req.ID
		m.Question = req.Question
		m.Rcode = dns.RcodeNameError
		// SOA with Minttl=60 — negative TTL should be capped to that.
		soa, _ := dns.New("example.com. 3600 IN SOA ns. mail. 1 1800 600 86400 60")
		m.Ns = []dns.RR{soa}
		return m, nil
	})

	lru := cache.NewLRU(10)
	r := cachestage.New(next, lru, defaults())

	req := dns.NewMsg("nx.example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, uint16(dns.RcodeNameError), resp.Rcode)
	require.Equal(t, 1, lru.Len(), "negative response should be cached")
}
