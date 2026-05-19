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
