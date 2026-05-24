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

func (c *captureQL) Enabled() bool { return true }

type disabledQL struct{}

func (disabledQL) LogQuery(_ ...slog.Attr) {}
func (disabledQL) Enabled() bool           { return false }

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

// stubResolver returns a preallocated response so callers that exercise the
// chain in a loop (benchmarks) and callers that need a pointer-comparable
// terminal resolver (identity tests) can share one type.
type stubResolver struct{ resp *dns.Msg }

func (s *stubResolver) Resolve(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
	return s.resp, nil
}

func TestQLog_EmitsClientAndProtoWhenPresent(t *testing.T) {
	cap := &captureQL{}
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		return r, nil
	})
	r := qlog.New(next, cap)

	ctx := resolver.WithClientInfo(context.Background(), resolver.ClientInfo{
		Addr:  "192.0.2.5:54321",
		Proto: "udp",
	})
	req := dns.NewMsg("example.com.", dns.TypeA)
	_, err := r.Resolve(ctx, req)
	require.NoError(t, err)

	require.Len(t, cap.entries, 1)
	got := map[string]string{}
	for _, a := range cap.entries[0] {
		got[a.Key] = a.Value.String()
	}
	require.Equal(t, "192.0.2.5:54321", got["client"])
	require.Equal(t, "udp", got["proto"])
}

func TestQLog_OmitsClientAndProtoWhenAbsent(t *testing.T) {
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
	for _, a := range cap.entries[0] {
		require.NotEqual(t, "client", a.Key, "client must be omitted when absent")
		require.NotEqual(t, "proto", a.Key, "proto must be omitted when absent")
	}
}

func TestQLog_DisabledReturnsNextUnchanged(t *testing.T) {
	next := &stubResolver{}
	r := qlog.New(next, disabledQL{})
	require.Same(t, resolver.Resolver(next), r, "disabled qlog must not wrap next")
}

func TestQLog_EmitsUpstreamAttrsWhenMarkerSet(t *testing.T) {
	cap := &captureQL{}
	next := resolver.ResolverFunc(func(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
		resolver.MarkUpstream(ctx, "https://cloudflare-dns.com/dns-query", "doh")
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		return r, nil
	})
	r := qlog.New(next, cap)

	ctx, _ := resolver.WithUpstreamMarker(context.Background())
	req := dns.NewMsg("example.com.", dns.TypeA)
	_, err := r.Resolve(ctx, req)
	require.NoError(t, err)

	require.Len(t, cap.entries, 1)
	got := map[string]string{}
	for _, a := range cap.entries[0] {
		got[a.Key] = a.Value.String()
	}
	require.Equal(t, "https://cloudflare-dns.com/dns-query", got["upstream"])
	require.Equal(t, "doh", got["upstream_protocol"])
}

func TestQLog_OmitsUpstreamAttrsWhenMarkerUnset(t *testing.T) {
	cap := &captureQL{}
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		// No MarkUpstream call — simulates cache hit / block / coalesce piggyback.
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
	for _, a := range cap.entries[0] {
		require.NotEqual(t, "upstream", a.Key, "upstream must be omitted when marker unset")
		require.NotEqual(t, "upstream_protocol", a.Key, "upstream_protocol must be omitted when marker unset")
	}
}

func BenchmarkQLog_Baseline(b *testing.B) {
	next := &stubResolver{resp: new(dns.Msg)}
	req := dns.NewMsg("example.com.", dns.TypeA)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = next.Resolve(ctx, req)
	}
}

func BenchmarkQLog_Disabled(b *testing.B) {
	next := &stubResolver{resp: new(dns.Msg)}
	r := qlog.New(next, disabledQL{})
	req := dns.NewMsg("example.com.", dns.TypeA)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Resolve(ctx, req)
	}
}

func BenchmarkQLog_EnabledNoop(b *testing.B) {
	next := &stubResolver{resp: new(dns.Msg)}
	r := qlog.New(next, &captureQL{})
	req := dns.NewMsg("example.com.", dns.TypeA)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Resolve(ctx, req)
	}
}
