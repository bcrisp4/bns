package upstream_test

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"github.com/bcrisp4/bns/internal/resolver"
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

func TestPool_MarksUpstreamOnSuccess(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true

	primary := &fakeUpstream{name: "primary", resp: ok}
	p := upstream.NewPool([]upstream.Upstream{primary}, nil)

	ctx, _ := resolver.WithUpstreamMarker(context.Background())
	req := dns.NewMsg("example.com.", dns.TypeA)

	_, err := p.Exchange(ctx, req)
	require.NoError(t, err)

	info, present := resolver.UpstreamInfoFrom(ctx)
	require.True(t, present)
	require.Equal(t, "primary", info.Name)
	require.Equal(t, "udp", info.Protocol)
}

func TestPool_NoMarkOnAllFail(t *testing.T) {
	p := upstream.NewPool([]upstream.Upstream{
		&fakeUpstream{name: "a", err: errors.New("a-fail")},
		&fakeUpstream{name: "b", err: errors.New("b-fail")},
	}, nil)

	ctx, _ := resolver.WithUpstreamMarker(context.Background())
	_, err := p.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
	require.Error(t, err)

	_, present := resolver.UpstreamInfoFrom(ctx)
	require.False(t, present, "marker must stay unset when no upstream succeeds")
}

func TestPool_MarksSecondaryOnFailover(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true

	p := upstream.NewPool([]upstream.Upstream{
		&fakeUpstream{name: "primary", err: errors.New("primary-down")},
		&fakeUpstream{name: "secondary", resp: ok},
	}, nil)

	ctx, _ := resolver.WithUpstreamMarker(context.Background())
	_, err := p.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
	require.NoError(t, err)

	info, present := resolver.UpstreamInfoFrom(ctx)
	require.True(t, present)
	require.Equal(t, "secondary", info.Name)
}
