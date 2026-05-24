package chain_test

import (
	"context"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver/chain"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type stubUpstream struct{ resp *dns.Msg }

func (s *stubUpstream) Exchange(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
	r := s.resp.Copy()
	r.ID = req.ID
	return r, nil
}

func (s *stubUpstream) Name() string     { return "stub" }
func (s *stubUpstream) Protocol() string { return "udp" }

func TestBuild_BlocklistShortCircuits(t *testing.T) {
	okResp := new(dns.Msg)
	okResp.Response = true

	deps := chain.Deps{
		Upstream:  upstream.NewPool([]upstream.Upstream{&stubUpstream{resp: okResp}}, nil),
		Cache:     cache.NewLRU(10),
		CacheCfg:  config.Cache{Capacity: 10, MaxTTL: 1 << 30, NegativeTTLMax: 1 << 30},
		Blocklist: blocklist.NewHolder(blocklist.NewMatcher([]string{"ads.example.com"})),
		QueryLog:  logging.QueryLogger(config.QueryLog{Enabled: false}, nil),
		Metrics:   metrics.New(prometheus.NewRegistry()),
	}
	r := chain.Build(deps)

	req := dns.NewMsg("ads.example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, uint16(dns.RcodeNameError), resp.Rcode)
}
