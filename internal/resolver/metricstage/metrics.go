// Package metricstage is the outermost stage of the resolver chain.
// It times every query, records counters, and recovers from panics
// (turning them into a clean error so the handler can write SERVFAIL).
package metricstage

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
)

type stage struct {
	next resolver.Resolver
	m    *metrics.Metrics
}

// New wraps next with the metrics + panic-recovery stage.
func New(next resolver.Resolver, m *metrics.Metrics) resolver.Resolver {
	return &stage{next: next, m: m}
}

func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (resp *dns.Msg, err error) {
	start := time.Now()

	defer func() {
		if r := recover(); r != nil {
			s.m.PanicsTotal.Inc()
			err = fmt.Errorf("panic: %v", r)
			resp = nil
		}
		qtype := "other"
		if len(req.Question) > 0 {
			qtype = metrics.NormalizeQType(dns.TypeToString[dns.RRToType(req.Question[0])])
		}
		outcome := outcomeFor(resp, err)
		s.m.QueriesTotal.WithLabelValues(outcome, qtype).Inc()
		s.m.QueryDurationSeconds.WithLabelValues(outcome).
			Observe(time.Since(start).Seconds())
	}()

	resp, err = s.next.Resolve(ctx, req)
	return resp, err
}

func outcomeFor(resp *dns.Msg, err error) string {
	switch {
	case err != nil:
		return "error"
	case resp == nil:
		return "error"
	case resp.Rcode == uint16(dns.RcodeNameError):
		return "blocked"
	default:
		return "forwarded"
	}
}
