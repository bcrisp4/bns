// Package blocklist loads, matches, and reloads ad/tracker blocklists.
package blocklist

import (
	"strings"
)

// ParseLine parses a single line from a blocklist file and returns the
// canonicalised FQDN (lowercased, trailing dot stripped) plus ok=true if
// the line represents a usable blocklist entry. ok=false means the line
// was a comment, blank, or malformed and should be skipped.
//
// Accepted input formats (union of hagezi domains/ and hosts/):
//
//	example.com
//	example.com.
//	0.0.0.0 example.com
//	127.0.0.1 example.com
//	:: example.com
//	::1 example.com
//
// Comment markers (entire line skipped): '#', ';', '!'.
func ParseLine(line string) (string, bool) {
	// Strip UTF-8 BOM if present at file start.
	line = strings.TrimPrefix(line, "\xef\xbb\xbf")
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	switch line[0] {
	case '#', ';', '!':
		return "", false
	}

	// hosts format: drop the IP anchor if present.
	fields := strings.Fields(line)
	var domain string
	switch len(fields) {
	case 1:
		domain = fields[0]
	case 2:
		if isHostsAnchor(fields[0]) {
			domain = fields[1]
		} else {
			return "", false
		}
	default:
		return "", false
	}

	domain = canonicalise(domain)
	if !isValidFQDN(domain) {
		return "", false
	}
	return domain, true
}

func isHostsAnchor(s string) bool {
	switch s {
	case "0.0.0.0", "127.0.0.1", "::", "::1":
		return true
	}
	return false
}

// isValidFQDN reports whether s is a syntactically valid ASCII DNS name:
// total length <= 253, each label 1..63 ASCII letters/digits/hyphens,
// labels separated by '.', no leading/trailing hyphen on any label.
// hagezi pre-converts IDNs to punycode, so we deliberately reject non-ASCII.
func isValidFQDN(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if l := len(label); l == 0 || l > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= '0' && c <= '9':
			case c == '-':
			default:
				return false
			}
		}
	}
	return true
}
