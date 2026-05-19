package cache_test

import (
	"net/netip"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/stretchr/testify/require"
)

// buildA returns a DNS response message with a single A record.
// name must be fully qualified (trailing dot).
func buildA(name string, ttl uint32, ip string) *dns.Msg {
	m := new(dns.Msg)
	m.Response = true
	// Question section: a bare A RR with only header fields set.
	q := &dns.A{Hdr: dns.Header{Name: name, Class: dns.ClassINET}}
	m.Question = []dns.RR{q}
	// Answer section: a full A RR with TTL and address.
	a := &dns.A{
		Hdr: dns.Header{Name: name, Class: dns.ClassINET, TTL: ttl},
		A:   rdata.A{Addr: netip.MustParseAddr(ip)},
	}
	m.Answer = []dns.RR{a}
	return m
}

// questionRR builds a minimal question RR (used as the Key argument).
func questionRR(name string, t uint16, class uint16) dns.RR {
	rr := dns.TypeToRR[t]()
	rr.Header().Name = name
	rr.Header().Class = class
	return rr
}

func TestLRU_StoreThenGet(t *testing.T) {
	c := cache.NewLRU(10)
	q := questionRR("example.com.", dns.TypeA, dns.ClassINET)
	msg := buildA("example.com.", 300, "1.2.3.4")

	c.Store(cache.Key(q), msg, 300*time.Second)
	got, ok := c.Get(cache.Key(q))
	require.True(t, ok)
	require.NotNil(t, got)
	require.Equal(t, "example.com.", got.Question[0].Header().Name)
}

func TestLRU_GetReturnsDeepCopy(t *testing.T) {
	c := cache.NewLRU(10)
	q := questionRR("example.com.", dns.TypeA, dns.ClassINET)
	msg := buildA("example.com.", 300, "1.2.3.4")
	c.Store(cache.Key(q), msg, 300*time.Second)

	got1, _ := c.Get(cache.Key(q))
	got2, _ := c.Get(cache.Key(q))
	require.NotSame(t, got1, got2, "Get must return independent copies")

	got1.Answer[0].Header().TTL = 1
	require.NotEqual(t, uint32(1), got2.Answer[0].Header().TTL, "mutating one copy must not affect another")
}

func TestLRU_StoreDeepCopiesInput(t *testing.T) {
	c := cache.NewLRU(10)
	q := questionRR("example.com.", dns.TypeA, dns.ClassINET)
	msg := buildA("example.com.", 300, "1.2.3.4")
	c.Store(cache.Key(q), msg, 300*time.Second)

	// Caller mutates after store -- cached entry must be untouched.
	msg.Answer[0].Header().TTL = 1

	got, ok := c.Get(cache.Key(q))
	require.True(t, ok)
	require.NotEqual(t, uint32(1), got.Answer[0].Header().TTL)
}

func TestLRU_Expiry(t *testing.T) {
	c := cache.NewLRU(10)
	q := questionRR("example.com.", dns.TypeA, dns.ClassINET)
	c.Store(cache.Key(q), buildA("example.com.", 300, "1.2.3.4"), 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)
	_, ok := c.Get(cache.Key(q))
	require.False(t, ok, "expired entry should miss")
	require.Equal(t, 0, c.Len(), "expired entry should evict on miss")
}

func TestLRU_TTLDecrementsByAge(t *testing.T) {
	c := cache.NewLRU(10)
	q := questionRR("example.com.", dns.TypeA, dns.ClassINET)
	c.Store(cache.Key(q), buildA("example.com.", 300, "1.2.3.4"), 300*time.Second)

	time.Sleep(15 * time.Millisecond)
	got, ok := c.Get(cache.Key(q))
	require.True(t, ok)
	// Original TTL was 300; after ~15ms should be 299 or 300, never above 300.
	require.LessOrEqual(t, got.Answer[0].Header().TTL, uint32(300))
}

func TestLRU_EvictionWhenFull(t *testing.T) {
	c := cache.NewLRU(2)
	qa := questionRR("a.", dns.TypeA, dns.ClassINET)
	qb := questionRR("b.", dns.TypeA, dns.ClassINET)
	qc := questionRR("c.", dns.TypeA, dns.ClassINET)
	c.Store(cache.Key(qa), buildA("a.", 60, "1.1.1.1"), time.Minute)
	c.Store(cache.Key(qb), buildA("b.", 60, "1.1.1.1"), time.Minute)
	c.Store(cache.Key(qc), buildA("c.", 60, "1.1.1.1"), time.Minute)

	_, okA := c.Get(cache.Key(qa))
	_, okB := c.Get(cache.Key(qb))
	_, okC := c.Get(cache.Key(qc))
	require.False(t, okA, "a was LRU and should have been evicted")
	require.True(t, okB)
	require.True(t, okC)
	require.Equal(t, 2, c.Len())
}
