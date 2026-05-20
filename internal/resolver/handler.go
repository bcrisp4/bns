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
	ctx = withClientInfoFromWriter(ctx, w)
	resp, err := h.r.Resolve(ctx, req)
	if err != nil || resp == nil {
		resp = servfail(req)
	}
	// m.WriteTo requires w to implement dns.ResponseWriter; it packs and writes.
	_, _ = resp.WriteTo(w)
}

// withClientInfoFromWriter stashes the writer's transport endpoint in ctx
// for downstream stages. A nil RemoteAddr skips the install to avoid
// logging a fabricated address.
func withClientInfoFromWriter(ctx context.Context, w dns.ResponseWriter) context.Context {
	remote := w.RemoteAddr()
	if remote == nil {
		return ctx
	}
	info := ClientInfo{Addr: remote.String()}
	if local := w.LocalAddr(); local != nil {
		info.Proto = local.Network()
	}
	return WithClientInfo(ctx, info)
}

func servfail(req *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.Response = true
	resp.ID = req.ID
	resp.Question = req.Question
	resp.Rcode = dns.RcodeServerFailure
	return resp
}
