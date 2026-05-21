package blocklist

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

// parseBody is the byte-slice entry point shared by HTTPSource.Load and
// Fetcher.FetchOne (which validates the body before writing the cache).
func parseBody(body []byte) []string {
	out, _ := parseReader(context.Background(), bytes.NewReader(body))
	return out
}

// HTTPSource is a Source whose entries come from a remote URL but whose
// Load reads only from the on-disk cache. The cache is populated by a
// separate Fetcher goroutine; HTTPSource never makes network calls,
// keeping Load synchronous and the request-serving path off the network.
type HTTPSource struct {
	name  string
	url   string
	store *CacheStore
}

var _ Source = (*HTTPSource)(nil)

// NewHTTPSource constructs an HTTPSource. name is used as the metric
// label and log field; url is the remote location and the cache key.
func NewHTTPSource(name, url string, store *CacheStore) *HTTPSource {
	return &HTTPSource{name: name, url: url, store: store}
}

// Name returns the operator-supplied source name.
func (s *HTTPSource) Name() string { return s.name }

// URL returns the configured remote URL.
func (s *HTTPSource) URL() string { return s.url }

// Load parses the cached body and returns canonicalised FQDNs. Returns
// an empty slice (no error) when no cache file exists; the Fetcher will
// populate it asynchronously and a later Reload will pick it up.
func (s *HTTPSource) Load(_ context.Context) ([]string, error) {
	body, _, err := s.store.Read(s.url)
	if errors.Is(err, ErrCacheMiss) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("http source %q: %w", s.name, err)
	}
	return parseBody(body), nil
}
