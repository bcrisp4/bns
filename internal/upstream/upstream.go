// Package upstream defines the Upstream interface and concrete clients
// used by the forwarder stage of the resolver chain.
package upstream

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Upstream sends a DNS query to a configured upstream resolver and returns
// the response. Implementations MUST NOT mutate req.
//
// Name and Protocol provide identity for metrics labels and per-query
// log attribution. Name is the operator-meaningful identifier (e.g.
// "1.1.1.1:53" for UDP, the DoH URL for DoH). Protocol is the transport
// kind ("udp" | "doh"). Both MUST be cheap and safe to call concurrently.
type Upstream interface {
	Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
	Name() string
	Protocol() string
}
