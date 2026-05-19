package blocklist

import "strings"

// Matcher answers "is this domain blocked?" via exact + parent-walk lookup.
// A blocked entry "ads.example.com" matches the exact name and any subdomain.
//
// Matcher is read-only after construction. To reload, build a new Matcher
// and atomically swap it via Holder.
type Matcher struct {
	set map[string]struct{}
}

// NewMatcher builds a Matcher from the given FQDNs. Duplicates are tolerated.
// Empty strings are dropped. Inputs are canonicalised (lowercased, trailing
// dot stripped) so callers can pass raw lines without preprocessing.
func NewMatcher(entries []string) *Matcher {
	set := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		e = canonicalise(e)
		if e == "" {
			continue
		}
		set[e] = struct{}{}
	}
	return &Matcher{set: set}
}

// Match reports whether qname is blocked. qname is canonicalised first,
// then probed against each parent label. O(labels) hash lookups per call.
func (m *Matcher) Match(qname string) bool {
	q := canonicalise(qname)
	if q == "" {
		return false
	}
	for {
		if _, ok := m.set[q]; ok {
			return true
		}
		i := strings.IndexByte(q, '.')
		if i < 0 {
			return false
		}
		q = q[i+1:]
	}
}

// Size returns the number of distinct FQDNs in the matcher.
func (m *Matcher) Size() int {
	return len(m.set)
}

func canonicalise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimSuffix(s, ".")
}
