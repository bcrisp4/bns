package qlog_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/qlog"
	"github.com/stretchr/testify/require"
)

type captureQL struct {
	mu      sync.Mutex
	entries [][]slog.Attr
}

func (c *captureQL) LogQuery(attrs ...slog.Attr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, append([]slog.Attr(nil), attrs...))
}

func TestQLog_EmitsQnameAndOutcome(t *testing.T) {
	cap := &captureQL{}
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		return r, nil
	})
	r := qlog.New(next, cap)

	req := dns.NewMsg("example.com.", dns.TypeA)
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)

	require.Len(t, cap.entries, 1)
	attrs := cap.entries[0]

	var qname, outcome string
	for _, a := range attrs {
		switch a.Key {
		case "qname":
			qname = a.Value.String()
		case "outcome":
			outcome = a.Value.String()
		}
	}
	require.Equal(t, "example.com.", qname)
	require.Equal(t, "forwarded", outcome)
}
