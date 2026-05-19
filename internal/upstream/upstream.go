// Package upstream defines the Upstream interface and concrete clients
// used by the forwarder stage of the resolver chain.
package upstream

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Upstream sends a DNS query to a configured upstream resolver and returns
// the response. Implementations MUST NOT mutate req.
type Upstream interface {
	Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}
