// Package coalesce deduplicates concurrent identical in-flight DNS queries
// into a single call to the next stage.
//
// Implementation today: golang.org/x/sync/singleflight. The package name
// hides this so the impl can change later without API or dashboard churn.
package coalesce

import (
	"context"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"golang.org/x/sync/singleflight"
)

type stage struct {
	next resolver.Resolver
	g    singleflight.Group
	m    *metrics.Metrics // nil → no metrics
}

// New wraps next with a coalescing stage that deduplicates concurrent identical
// in-flight queries. When multiple goroutines ask the same question at the same
// time, only one call reaches next; the rest block and receive independent deep
// copies of the shared result.
//
// mtr may be nil, in which case no metrics are recorded.
func New(next resolver.Resolver, mtr *metrics.Metrics) resolver.Resolver {
	return &stage{next: next, m: mtr}
}

func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(req.Question) != 1 {
		// Multi-question (or empty) messages are not coalesced; just pass through.
		return s.next.Resolve(ctx, req)
	}
	key := cache.Key(req.Question[0])
	v, err, shared := s.g.Do(key, func() (any, error) {
		return s.next.Resolve(ctx, req)
	})
	if shared && s.m != nil {
		// This goroutine piggybacked on an in-flight call; count the coalescence.
		s.m.CoalescedQueriesTotal.Inc()
	}
	if err != nil {
		return nil, err
	}
	// Deep-copy so each caller owns an independent response. Reuse cache.CloneMsg
	// because dns.Msg.Copy() in v2 is shallow on RR slices — callers mutating
	// Header().TTL on their copy would otherwise corrupt siblings' copies.
	resp := cache.CloneMsg(v.(*dns.Msg))
	resp.ID = req.ID
	return resp, nil
}
