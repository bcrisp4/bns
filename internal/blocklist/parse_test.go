package blocklist_test

import (
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestParseLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain domain", "example.com", "example.com", true},
		{"uppercase", "Example.COM", "example.com", true},
		{"trailing dot", "example.com.", "example.com", true},
		{"hosts v4", "0.0.0.0 example.com", "example.com", true},
		{"hosts v4 alt", "127.0.0.1 example.com", "example.com", true},
		{"hosts v6", ":: example.com", "example.com", true},
		{"hosts v6 alt", "::1 example.com", "example.com", true},
		{"leading whitespace", "   example.com   ", "example.com", true},
		{"comment hash", "# a comment", "", false},
		{"comment semi", "; bind-style", "", false},
		{"comment bang", "! adblock-style", "", false},
		{"blank", "", "", false},
		{"whitespace only", "   \t  ", "", false},
		{"bad chars", "exa mple.com", "", false},
		{"too long", longLabel(254), "", false},
		{"label too long", longLabel(64) + ".com", "", false},
		{"bom prefix", "\xef\xbb\xbfexample.com", "example.com", true},
		{"punycode", "xn--bcher-kva.de", "xn--bcher-kva.de", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := blocklist.ParseLine(tc.in)
			require.Equal(t, tc.ok, ok, "ok mismatch for %q -> %q", tc.in, got)
			if ok {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

func longLabel(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
