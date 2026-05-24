package resolver

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Outcome classifies the result of a Resolve call into a low-cardinality
// label suitable for metrics and logs. Values: "blocked", "nxdomain",
// "forwarded", "error".
//
// "blocked" means the blocklist stage synthesised an NXDOMAIN. "nxdomain"
// means an upstream returned NXDOMAIN for a name we did not block. The
// distinction is delivered via context: BlockMarker(ctx) records the block
// at the stage that performed it; Outcome reads it back at the metrics or
// query-log stage.
//
// Cache hit vs miss is not distinguished — both are returned as "forwarded"
// because the cache stage doesn't propagate that decision up the chain.
// Operators infer hit rate from bns_upstream_queries_total vs
// bns_queries_total.
func Outcome(ctx context.Context, resp *dns.Msg, err error) string {
	switch {
	case err != nil:
		return "error"
	case resp == nil:
		return "error"
	case resp.Rcode == uint16(dns.RcodeNameError):
		if marker, ok := ctx.Value(blockMarkerKey{}).(*bool); ok && marker != nil && *marker {
			return "blocked"
		}
		return "nxdomain"
	default:
		return "forwarded"
	}
}

// blockMarkerKey is the ctx key for the per-query block marker. Unexported
// type prevents collisions across packages.
type blockMarkerKey struct{}

// WithBlockMarker installs a fresh block marker in ctx and returns the
// new context plus a pointer the caller can later inspect. The metrics
// stage calls this on every query so downstream stages can flag a block.
func WithBlockMarker(ctx context.Context) (context.Context, *bool) {
	var marked bool
	return context.WithValue(ctx, blockMarkerKey{}, &marked), &marked
}

// MarkBlocked sets the block marker on ctx (if present). The blocklist
// stage calls this when it synthesises an NXDOMAIN. Safe to call on a
// ctx with no marker installed (no-op).
func MarkBlocked(ctx context.Context) {
	if m, ok := ctx.Value(blockMarkerKey{}).(*bool); ok && m != nil {
		*m = true
	}
}

// ClientInfo describes the transport endpoint that issued a query. The
// handler adapter installs it in ctx so resolver stages can read it without
// depending on the dns.ResponseWriter.
type ClientInfo struct {
	Addr  string // host:port form, e.g. "192.0.2.5:54321"
	Proto string // "udp" or "tcp"
}

type clientInfoKey struct{}

func WithClientInfo(ctx context.Context, info ClientInfo) context.Context {
	return context.WithValue(ctx, clientInfoKey{}, info)
}

func ClientInfoFrom(ctx context.Context) (ClientInfo, bool) {
	v, ok := ctx.Value(clientInfoKey{}).(ClientInfo)
	return v, ok
}

// UpstreamInfo describes the upstream that served a query. Recorded by
// Pool.Exchange on success; read by the qlog stage so each forwarded
// query log line identifies which forwarder served it.
//
// Empty Name signals "no upstream was used" (cache hit, blocked query,
// coalesce piggyback).
type UpstreamInfo struct {
	Name     string // e.g. "1.1.1.1:53" for UDP, the URL for DoH
	Protocol string // "udp" | "doh"
}

type upstreamInfoKey struct{}

// WithUpstreamMarker installs a fresh upstream-info marker in ctx and
// returns the new context plus a pointer the caller can later inspect.
// The metrics stage calls this on every query so downstream stages
// (Pool) can record which upstream served the query.
func WithUpstreamMarker(ctx context.Context) (context.Context, *UpstreamInfo) {
	var info UpstreamInfo
	return context.WithValue(ctx, upstreamInfoKey{}, &info), &info
}

// MarkUpstream sets the upstream marker on ctx (if present). Pool calls
// this immediately after a successful per-upstream Exchange. Safe to
// call on a ctx with no marker installed (no-op).
func MarkUpstream(ctx context.Context, name, protocol string) {
	if info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo); ok && info != nil {
		info.Name = name
		info.Protocol = protocol
	}
}

// UpstreamInfoFrom returns the recorded upstream info from ctx. Returns
// (UpstreamInfo{}, false) when no marker is installed OR when the
// marker exists but has not been set (Name == "") — the qlog stage
// uses this to omit upstream attrs cleanly for cache hits and blocks.
func UpstreamInfoFrom(ctx context.Context) (UpstreamInfo, bool) {
	info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo)
	if !ok || info == nil || info.Name == "" {
		return UpstreamInfo{}, false
	}
	return *info, true
}
