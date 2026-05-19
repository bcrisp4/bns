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

// New wraps next; emits a "query" line per call through q.
func New(next resolver.Resolver, q logging.QueryLog) resolver.Resolver {
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

	s.q.LogQuery(
		slog.String("qname", qname),
		slog.String("qtype", qtype),
		slog.String("outcome", resolver.Outcome(ctx, resp, err)),
		slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000),
	)
	return resp, err
}
