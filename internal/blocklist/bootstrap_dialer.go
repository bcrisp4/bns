package blocklist

import (
	"context"
	"net"
	"sync/atomic"
)

// NewBootstrapResolver returns a *net.Resolver that resolves DNS names
// by dialing one of upstreamAddrs directly, bypassing the system stub.
// The stdlib pure-Go resolver handles DNS framing and TCP fallback; the
// dialer only supplies a connection to an upstream of our choosing.
//
// upstreamAddrs are "host:port" forms identical to those in the BNS
// upstream pool. They are used for their IP set only — this resolver
// MUST NOT be wired through the BNS resolver chain (deadlock).
func NewBootstrapResolver(upstreamAddrs []string) *net.Resolver {
	if len(upstreamAddrs) == 0 {
		return net.DefaultResolver
	}
	addrs := append([]string(nil), upstreamAddrs...)
	var rr uint32
	// dial ignores the stdlib-supplied `address` (which would be the
	// system resolver target it picked) and substitutes one of our
	// configured upstream addrs instead.
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var (
			lastErr error
			d       net.Dialer
		)
		// Round-robin over upstreams; first reachable wins. Mirrors Pool's
		// "try each in order" behaviour but is dial-only — DNS framing is
		// the stdlib resolver's job.
		start := int(atomic.AddUint32(&rr, 1)) % len(addrs)
		for i := 0; i < len(addrs); i++ {
			addr := addrs[(start+i)%len(addrs)]
			c, err := d.DialContext(ctx, network, addr)
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &net.Resolver{
		PreferGo: true,
		Dial:     dial,
	}
}
