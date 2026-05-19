// Package forward is the terminal stage of the resolver chain: it hands
// the request to an upstream and returns the response.
package forward

import (
	"context"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/upstream"
)

type stage struct {
	up upstream.Upstream
}

// New returns a Resolver that forwards to up.
func New(up upstream.Upstream) resolver.Resolver {
	return &stage{up: up}
}

func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	return s.up.Exchange(ctx, req)
}
