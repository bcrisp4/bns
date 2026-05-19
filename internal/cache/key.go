// Package cache is the in-tree LRU cache used by the resolver chain.
//
// The cache is TTL-aware: entries carry a per-item expiresAt computed from
// the upstream response. Stored *dns.Msg are deep-copies (see Store);
// returned *dns.Msg are deep-copies too (see Get). This is load-bearing —
// the miekg server returns *dns.Msg buffers to a sync.Pool, so aliasing a
// cached entry with a live request would cause race conditions and corruption.
package cache

import (
	"strconv"
	"strings"

	"codeberg.org/miekg/dns"
)

// Key returns the canonical cache key for a DNS question RR.
//
//	<lowercased-qname>|<qtype>|<qclass>
//
// Trailing dot is preserved so we match the on-the-wire form.
//
// In miekg/dns v2, the question section is represented as a []RR. The type
// is derived from the concrete Go type via [dns.RRToType]; the name and class
// live in the [dns.Header].
func Key(q dns.RR) string {
	hdr := q.Header()
	var sb strings.Builder
	sb.Grow(len(hdr.Name) + 12)
	sb.WriteString(strings.ToLower(hdr.Name))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatUint(uint64(dns.RRToType(q)), 10))
	sb.WriteByte('|')
	sb.WriteString(strconv.FormatUint(uint64(hdr.Class), 10))
	return sb.String()
}
