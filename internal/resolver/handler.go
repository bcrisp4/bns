package resolver

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Handler is a dns.Handler that delegates to a Resolver. On error or nil
// response it writes SERVFAIL so the client always sees an answer.
type Handler struct {
	r Resolver
}

// NewHandler wraps r as a dns.Handler.
func NewHandler(r Resolver) *Handler {
	return &Handler{r: r}
}

// ServeDNS implements dns.Handler.
func (h *Handler) ServeDNS(ctx context.Context, w dns.ResponseWriter, req *dns.Msg) {
	resp, err := h.r.Resolve(ctx, req)
	if err != nil || resp == nil {
		resp = servfail(req)
	}
	// m.WriteTo requires w to implement dns.ResponseWriter; it packs and writes.
	_, _ = resp.WriteTo(w)
}

func servfail(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.Response = true
	resp.ID = req.ID
	resp.Question = req.Question
	resp.Rcode = dns.RcodeServerFailure
	return resp
}
