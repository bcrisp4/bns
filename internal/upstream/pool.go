package upstream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
)

// Pool tries each Upstream in order, returning the first success.
// Errors are aggregated; the returned error wraps all of them.
type Pool struct {
	upstreams []Upstream
	names     []string        // parallel to upstreams; used as the "upstream" label value
	m         *metrics.Metrics // nil → no metrics
}

// NewPool constructs a Pool over the given Upstreams. names must be the same
// length as ups (used as the Prometheus "upstream" label); pass nil to omit
// the label (names will fall back to the upstream index). mtr may be nil, in
// which case no metrics are recorded.
func NewPool(ups []Upstream, names []string, mtr *metrics.Metrics) *Pool {
	return &Pool{upstreams: ups, names: names, m: mtr}
}

// name returns the human-readable label for upstream i, falling back to the
// index string if names was not provided or is too short.
func (p *Pool) name(i int) string {
	if i < len(p.names) {
		return p.names[i]
	}
	return fmt.Sprintf("upstream[%d]", i)
}

// Exchange tries each upstream in order. Returns the first success.
// If all fail, returns errors.Join of all failures.
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(p.upstreams) == 0 {
		return nil, errors.New("upstream pool: no upstreams configured")
	}
	errs := make([]error, 0, len(p.upstreams))
	for i, u := range p.upstreams {
		start := time.Now()
		resp, err := u.Exchange(ctx, req)
		elapsed := time.Since(start).Seconds()
		name := p.name(i)
		if p.m != nil {
			p.m.UpstreamDurationSeconds.WithLabelValues(name).Observe(elapsed)
			if err == nil {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, "ok").Inc()
			} else {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, "error").Inc()
			}
		}
		if err == nil {
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
