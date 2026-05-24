package upstream_test

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/stretchr/testify/require"
)

type fakeUpstream struct {
	name string
	resp *dns.Msg
	err  error
}

func (f *fakeUpstream) Exchange(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := new(dns.Msg)
	dnsutil.SetReply(r, req)
	r.Response = f.resp.Response
	return r, nil
}

func (f *fakeUpstream) Name() string     { return f.name }
func (f *fakeUpstream) Protocol() string { return "udp" }

func TestPool_PrimarySucceeds(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true
	p := upstream.NewPool([]upstream.Upstream{
		&fakeUpstream{name: "p", resp: ok},
		&fakeUpstream{name: "f", err: errors.New("should not be called")},
	}, nil)
	req := dns.NewMsg("example.com.", dns.TypeA)
	resp, err := p.Exchange(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Response)
}

func TestPool_FallbackOnError(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true
	p := upstream.NewPool([]upstream.Upstream{
		&fakeUpstream{name: "p", err: errors.New("boom")},
		&fakeUpstream{name: "f", resp: ok},
	}, nil)
	req := dns.NewMsg("example.com.", dns.TypeA)
	resp, err := p.Exchange(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Response)
}

func TestPool_AllFail(t *testing.T) {
	p := upstream.NewPool([]upstream.Upstream{
		&fakeUpstream{name: "a", err: errors.New("a-fail")},
		&fakeUpstream{name: "b", err: errors.New("b-fail")},
	}, nil)
	req := dns.NewMsg("example.com.", dns.TypeA)
	_, err := p.Exchange(context.Background(), req)
	require.Error(t, err)
}

func TestPool_EmptyIsError(t *testing.T) {
	p := upstream.NewPool(nil, nil)
	_, err := p.Exchange(context.Background(), new(dns.Msg))
	require.Error(t, err)
}
