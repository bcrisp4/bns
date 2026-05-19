package blocklist

import "context"

// Source loads raw FQDNs from somewhere. Implementations must return
// already-canonicalised (lowercased, no trailing dot) ASCII FQDNs.
//
// The MVP ships FileSource. Future implementations (URL fetcher, etc.)
// drop in here without touching the loader or matcher.
type Source interface {
	Load(ctx context.Context) ([]string, error)
}
