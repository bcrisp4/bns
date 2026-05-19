package upstream

import (
	"context"
	"errors"
	"fmt"

	"codeberg.org/miekg/dns"
)

// Pool tries each Upstream in order, returning the first success.
// Errors are aggregated; the returned error wraps all of them.
type Pool struct {
	upstreams []Upstream
}

// NewPool constructs a Pool over the given Upstreams. The first entry is
// the primary; subsequent entries are fallbacks tried in order.
func NewPool(ups []Upstream) *Pool {
	return &Pool{upstreams: ups}
}

// Exchange tries each upstream in order. Returns the first success.
// If all fail, returns errors.Join of all failures.
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(p.upstreams) == 0 {
		return nil, errors.New("upstream pool: no upstreams configured")
	}
	errs := make([]error, 0, len(p.upstreams))
	for i, u := range p.upstreams {
		resp, err := u.Exchange(ctx, req)
		if err == nil {
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("upstream[%d]: %w", i, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
