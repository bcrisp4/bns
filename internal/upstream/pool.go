package upstream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
)

// Pool tries each Upstream in declared order, returning the first success.
// Failover is sequential: secondary upstreams are consulted only when the
// primary returns an error. SERVFAIL/NXDOMAIN/REFUSED from a successful
// exchange are valid responses and do NOT trigger failover.
//
// Errors are aggregated via errors.Join. ctx cancellation short-circuits.
type Pool struct {
	upstreams []Upstream
	m         *metrics.Metrics // nil → no metrics
}

// NewPool constructs a Pool over ups. mtr may be nil.
func NewPool(ups []Upstream, mtr *metrics.Metrics) *Pool {
	return &Pool{upstreams: ups, m: mtr}
}

// Name returns "pool" — Pool itself is wrapped by the forwarder stage,
// not recorded directly as an upstream in metrics. Provided for interface
// completeness.
func (p *Pool) Name() string { return "pool" }

// Protocol returns "pool" for the same reason as Name.
func (p *Pool) Protocol() string { return "pool" }

// Exchange tries each upstream in order. Returns the first success.
// On success, records the winning upstream's name + protocol in ctx via
// resolver.MarkUpstream so the query-log stage can surface it.
// If all fail, returns errors.Join of all failures.
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(p.upstreams) == 0 {
		return nil, errors.New("upstream pool: no upstreams configured")
	}
	errs := make([]error, 0, len(p.upstreams))
	for _, u := range p.upstreams {
		start := time.Now()
		resp, err := u.Exchange(ctx, req)
		elapsed := time.Since(start).Seconds()
		name := u.Name()
		proto := u.Protocol()
		if p.m != nil {
			p.m.UpstreamDurationSeconds.WithLabelValues(name, proto).Observe(elapsed)
			if err == nil {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, proto, "ok").Inc()
			} else {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, proto, "error").Inc()
			}
		}
		if err == nil {
			resolver.MarkUpstream(ctx, name, proto)
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
