package metricstage_test

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/metricstage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricStage_CountsOutcomes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	ok := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		return r, nil
	})
	r := metricstage.New(ok, m)

	req := dns.NewMsg("example.com.", dns.TypeA)
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)

	got := testutil.ToFloat64(m.QueriesTotal.WithLabelValues("forwarded", "A"))
	require.Equal(t, 1.0, got)
}

func TestMetricStage_RecoversPanic(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	boom := resolver.ResolverFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
		panic("boom")
	})
	r := metricstage.New(boom, m)

	req := dns.NewMsg("example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.Error(t, err)
	require.Nil(t, resp)
	require.Equal(t, 1.0, testutil.ToFloat64(m.PanicsTotal))
}

func TestMetricStage_ErrorOutcomeAccounting(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	r := metricstage.New(resolver.ResolverFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
		return nil, errors.New("nope")
	}), m)

	req := dns.NewMsg("example.com.", dns.TypeA)
	_, _ = r.Resolve(context.Background(), req)
	require.Equal(t, 1.0, testutil.ToFloat64(m.QueriesTotal.WithLabelValues("error", "A")))
}

func TestMetricStage_UpstreamNXDOMAINLabelledNxdomain(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	upstreamNX := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		r.Rcode = uint16(dns.RcodeNameError)
		return r, nil
	})
	r := metricstage.New(upstreamNX, m)

	req := dns.NewMsg("does-not-exist.example.", dns.TypeA)
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1.0, testutil.ToFloat64(m.QueriesTotal.WithLabelValues("nxdomain", "A")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.QueriesTotal.WithLabelValues("blocked", "A")))
}

func TestMetricStage_BlockedNXDOMAINLabelledBlocked(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	blocked := resolver.ResolverFunc(func(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
		resolver.MarkBlocked(ctx)
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		r.Rcode = uint16(dns.RcodeNameError)
		return r, nil
	})
	r := metricstage.New(blocked, m)

	req := dns.NewMsg("ads.example.", dns.TypeA)
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1.0, testutil.ToFloat64(m.QueriesTotal.WithLabelValues("blocked", "A")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.QueriesTotal.WithLabelValues("nxdomain", "A")))
}
