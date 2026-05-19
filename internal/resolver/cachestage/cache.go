// Package cachestage is the cache lookup/store stage of the resolver chain.
//
// On every query it first checks the LRU cache. A hit short-circuits the
// chain and returns the stored response with the caller's ID stamped in.
// A miss delegates to the next Resolver and then stores the result according
// to TTL rules:
//
//   - Positive (NOERROR + answers): TTL = min(all-section-min, cfg.MaxTTL),
//     floored at cfg.MinTTL. Do not cache if no TTL information exists.
//   - Negative (NXDOMAIN / NODATA): TTL = min(SOA-MINIMUM, cfg.NegativeTTLMax).
//     Do not cache if no SOA is present in the authority section.
package cachestage

import (
	"context"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/resolver"
)

type stage struct {
	next  resolver.Resolver
	store *cache.LRU
	cfg   config.Cache
}

// New wraps next with a cache stage backed by store, governed by cfg.
func New(next resolver.Resolver, store *cache.LRU, cfg config.Cache) resolver.Resolver {
	return &stage{next: next, store: store, cfg: cfg}
}

// Resolve checks the cache for a stored answer. On a miss it delegates to
// next, then caches the response if the computed TTL is positive.
func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	// Only cache single-question queries — multi-question is exceedingly rare
	// and hard to key correctly; forward them without touching the cache.
	if len(req.Question) != 1 {
		return s.next.Resolve(ctx, req)
	}
	key := cache.Key(req.Question[0])

	// Cache hit: stamp the caller's ID and return the deep-copied entry.
	if resp, ok := s.store.Get(key); ok {
		resp.ID = req.ID
		return resp, nil
	}

	resp, err := s.next.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return resp, nil
	}

	ttl, _ := computeTTL(resp, s.cfg)
	if ttl > 0 {
		s.store.Store(key, resp, ttl)
	}
	return resp, nil
}

// computeTTL derives the cache duration and whether it is a negative entry.
//
// Positive responses: the minimum TTL across all RR sections is clamped to
// [cfg.MinTTL, cfg.MaxTTL]. Returns (0, false) if no RRs carry TTL info.
//
// Negative responses (NXDOMAIN or NODATA): the SOA MINIMUM field in the
// authority section determines the TTL, capped at cfg.NegativeTTLMax.
// Returns (0, true) if no SOA is present (do not cache, but signal negative).
func computeTTL(resp *dns.Msg, cfg config.Cache) (time.Duration, bool) {
	isNegative := resp.Rcode == uint16(dns.RcodeNameError) ||
		(resp.Rcode == uint16(dns.RcodeSuccess) && len(resp.Answer) == 0)

	if isNegative {
		if soa := findSOA(resp.Ns); soa != nil {
			ttl := time.Duration(soa.Minttl) * time.Second
			if ttl > cfg.NegativeTTLMax {
				ttl = cfg.NegativeTTLMax
			}
			return ttl, true
		}
		// No SOA → do not cache.
		return 0, true
	}

	// Positive response: use the minimum TTL across all RR sections.
	min := minTTL(resp)
	if min == 0 {
		return 0, false
	}
	dur := time.Duration(min) * time.Second
	if dur > cfg.MaxTTL {
		dur = cfg.MaxTTL
	}
	if dur < cfg.MinTTL {
		dur = cfg.MinTTL
	}
	return dur, false
}

// findSOA returns the first SOA record in rrs, or nil if none is present.
func findSOA(rrs []dns.RR) *dns.SOA {
	for _, rr := range rrs {
		if soa, ok := rr.(*dns.SOA); ok {
			return soa
		}
	}
	return nil
}

// minTTL returns the smallest TTL across all RR sections of m.
// Returns 0 if m contains no resource records.
func minTTL(m *dns.Msg) uint32 {
	var best uint32
	first := true
	for _, section := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range section {
			t := rr.Header().TTL
			if first || t < best {
				best = t
				first = false
			}
		}
	}
	if first {
		return 0
	}
	return best
}
