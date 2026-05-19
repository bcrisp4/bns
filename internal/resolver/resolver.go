// Package resolver defines the Resolver interface and a chain builder.
//
// The resolver chain is composed outside-in:
//
//	metrics → query-log → blocklist → cache → coalesce → forward
//
// Each stage either short-circuits with a synthesised response or delegates
// to its next Resolver. All stages are ctx-first; cancellation cascades.
package resolver

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Resolver answers a single DNS query.
//
// Implementations MUST:
//   - never mutate req
//   - return a response with Id and Question matching req
//   - propagate ctx cancellation
//   - return either (response, nil) or (nil, error); never (resp, err)
type Resolver interface {
	Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}

// ResolverFunc adapts a function to the Resolver interface.
type ResolverFunc func(ctx context.Context, req *dns.Msg) (*dns.Msg, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	return f(ctx, req)
}
