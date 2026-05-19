package forward_test

import (
	"context"
	"errors"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/resolver/forward"
	"github.com/stretchr/testify/require"
)

type fakeUpstream struct {
	resp *dns.Msg
	err  error
}

func (f *fakeUpstream) Exchange(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := f.resp.Copy()
	r.ID = req.ID
	return r, nil
}

func TestForward_PassesThrough(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true
	r := forward.New(&fakeUpstream{resp: ok})

	req := dns.NewMsg("example.com.", dns.TypeA)
	resp, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.True(t, resp.Response)
}

func TestForward_PropagatesError(t *testing.T) {
	r := forward.New(&fakeUpstream{err: errors.New("boom")})
	_, err := r.Resolve(context.Background(), new(dns.Msg))
	require.Error(t, err)
}
