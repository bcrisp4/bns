package blockstage_test

import (
	"context"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/blockstage"
	"github.com/stretchr/testify/require"
)

func TestBlockStage_BlockedReturnsNXDOMAIN(t *testing.T) {
	holder := blocklist.NewHolder(blocklist.NewMatcher([]string{"ads.example.com"}))
	next := resolver.ResolverFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
		t.Fatal("blocked queries must not reach next stage")
		return nil, nil
	})
	r := blockstage.New(next, holder)

	req := dns.NewMsg("foo.ads.example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, uint16(dns.RcodeNameError), resp.Rcode)
	require.Equal(t, req.ID, resp.ID)
	require.Equal(t, req.Question, resp.Question)
}

func TestBlockStage_NotBlockedPassesThrough(t *testing.T) {
	holder := blocklist.NewHolder(blocklist.NewMatcher([]string{"ads.example.com"}))
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		r := new(dns.Msg)
		r.Response = true
		r.ID = req.ID
		r.Question = req.Question
		return r, nil
	})
	r := blockstage.New(next, holder)

	req := dns.NewMsg("example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
}
