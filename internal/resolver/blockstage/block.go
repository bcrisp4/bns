// Package blockstage is the blocklist stage of the resolver chain.
// On a hit, it synthesises an NXDOMAIN response without calling next.
package blockstage

import (
	"context"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/resolver"
)

type stage struct {
	next   resolver.Resolver
	holder *blocklist.Holder
}

// New wraps next; on hits from holder it returns NXDOMAIN.
func New(next resolver.Resolver, h *blocklist.Holder) resolver.Resolver {
	return &stage{next: next, holder: h}
}

func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(req.Question) != 1 {
		return s.next.Resolve(ctx, req)
	}
	m := s.holder.Current()
	if m == nil {
		return s.next.Resolve(ctx, req)
	}
	qname := req.Question[0].Header().Name
	if !m.Match(qname) {
		return s.next.Resolve(ctx, req)
	}
	resp := new(dns.Msg)
	resp.Response = true
	resp.ID = req.ID
	resp.Question = req.Question
	resp.Rcode = uint16(dns.RcodeNameError)
	return resp, nil
}
