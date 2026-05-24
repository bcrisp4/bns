// Package qlog is the per-query logging stage. It emits one log line per
// query through the injected QueryLog.
package qlog

import (
	"context"
	"log/slog"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/resolver"
)

type stage struct {
	next resolver.Resolver
	q    logging.QueryLog
}

// New wraps next; emits a "query" line per call through q. When q is disabled
// (query log off) New returns next unchanged so the chain skips this stage
// entirely on the hot path.
func New(next resolver.Resolver, q logging.QueryLog) resolver.Resolver {
	if !q.Enabled() {
		return next
	}
	return &stage{next: next, q: q}
}

func (s *stage) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	start := time.Now()
	resp, err := s.next.Resolve(ctx, req)

	var qname, qtype string
	if len(req.Question) > 0 {
		q0 := req.Question[0]
		qname = q0.Header().Name
		// RRToType derives the uint16 type code from the concrete RR type;
		// TypeToString maps it to a human-readable string (e.g. "A", "AAAA").
		qtype = dns.TypeToString[dns.RRToType(q0)]
	}

	attrs := make([]slog.Attr, 4, 8)
	attrs[0] = slog.String("qname", qname)
	attrs[1] = slog.String("qtype", qtype)
	attrs[2] = slog.String("outcome", resolver.Outcome(ctx, resp, err))
	attrs[3] = slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000)
	// Skip when Addr is empty to avoid logging a placeholder.
	if info, ok := resolver.ClientInfoFrom(ctx); ok && info.Addr != "" {
		attrs = append(attrs, slog.String("client", info.Addr))
		if info.Proto != "" {
			attrs = append(attrs, slog.String("proto", info.Proto))
		}
	}
	if info, present := resolver.UpstreamInfoFrom(ctx); present {
		attrs = append(attrs,
			slog.String("upstream", info.Name),
			slog.String("upstream_protocol", info.Protocol),
		)
	}
	s.q.LogQuery(attrs...)
	return resp, err
}
