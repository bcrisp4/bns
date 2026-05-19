package cache_test

import (
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/stretchr/testify/require"
)

// makeQ builds a minimal question RR of the requested type with the given name.
func makeQ(name string, t uint16, class uint16) dns.RR {
	rr := dns.TypeToRR[t]()
	rr.Header().Name = name
	rr.Header().Class = class
	return rr
}

func TestKey_CanonicalisesQname(t *testing.T) {
	q1 := makeQ("Example.COM.", dns.TypeA, dns.ClassINET)
	q2 := makeQ("example.com.", dns.TypeA, dns.ClassINET)
	require.Equal(t, cache.Key(q1), cache.Key(q2))
}

func TestKey_TypeAndClassAffectKey(t *testing.T) {
	q1 := makeQ("example.com.", dns.TypeA, dns.ClassINET)
	q2 := makeQ("example.com.", dns.TypeAAAA, dns.ClassINET)
	require.NotEqual(t, cache.Key(q1), cache.Key(q2))
}
