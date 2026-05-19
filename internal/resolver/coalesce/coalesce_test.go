package coalesce_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/coalesce"
	"github.com/stretchr/testify/require"
)

func TestCoalesce_CollapsesIdenticalInFlight(t *testing.T) {
	var calls atomic.Int64
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		resp := new(dns.Msg)
		dnsutil.SetReply(resp, req)
		return resp, nil
	})
	r := coalesce.New(next)

	req := dns.NewMsg("example.com.", dns.TypeA)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := r.Resolve(context.Background(), req)
			require.NoError(t, err)
			require.True(t, resp.Response)
		}()
	}
	wg.Wait()

	require.EqualValues(t, 1, calls.Load(), "all 20 calls should have collapsed")
}

func TestCoalesce_ReturnsDeepCopies(t *testing.T) {
	next := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		dnsutil.SetReply(resp, req)
		rr, _ := dns.New("example.com. 60 IN A 1.2.3.4")
		resp.Answer = []dns.RR{rr}
		return resp, nil
	})
	r := coalesce.New(next)

	req := dns.NewMsg("example.com.", dns.TypeA)
	a, _ := r.Resolve(context.Background(), req)
	b, _ := r.Resolve(context.Background(), req)
	require.NotSame(t, a, b)
	a.Answer[0].Header().TTL = 1
	require.NotEqual(t, uint32(1), b.Answer[0].Header().TTL)
}
