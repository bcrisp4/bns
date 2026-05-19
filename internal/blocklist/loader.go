package blocklist

import (
	"context"
	"fmt"
)

// Loader composes one or more Sources into a single Matcher build pipeline.
//
// Use:
//
//	loader := NewLoader(sources)
//	matcher, raw, err := loader.Load(ctx)
//	holder.Swap(matcher)
//
// Load is safe to call concurrently with Match readers on the Holder; the
// swap is what readers actually see.
type Loader struct {
	sources []Source
}

// NewLoader constructs a Loader over the given sources. nil/empty is OK
// (Load returns an empty Matcher).
func NewLoader(sources []Source) *Loader {
	return &Loader{sources: sources}
}

// Load fetches every source, concatenates the entries, builds a fresh
// Matcher, and returns (matcher, totalRawEntries, error). If any source
// fails, the whole load fails and the caller should keep the previous
// Matcher installed.
func (l *Loader) Load(ctx context.Context) (*Matcher, int, error) {
	all := make([]string, 0, 64*1024)
	for i, s := range l.sources {
		entries, err := s.Load(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("source[%d]: %w", i, err)
		}
		all = append(all, entries...)
	}
	return NewMatcher(all), len(all), nil
}
