package blocklist_test

import (
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestMatcher_ExactAndSuffix(t *testing.T) {
	m := blocklist.NewMatcher([]string{"ads.example.com", "tracker.net"})

	cases := []struct {
		qname string
		want  bool
	}{
		{"ads.example.com", true},
		{"x.ads.example.com", true},
		{"a.b.c.ads.example.com", true},
		{"example.com", false}, // parent of a blocked entry is NOT blocked
		{"adsxexample.com", false},
		{"tracker.net", true},
		{"sub.tracker.net", true},
		{"nottracker.net", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.qname, func(t *testing.T) {
			require.Equal(t, tc.want, m.Match(tc.qname))
		})
	}
}

func TestMatcher_CanonicalisesInput(t *testing.T) {
	m := blocklist.NewMatcher([]string{"ads.example.com"})
	require.True(t, m.Match("ADS.Example.com."))
	require.True(t, m.Match(" ads.example.com "))
}

func TestMatcher_Size(t *testing.T) {
	m := blocklist.NewMatcher([]string{"a.com", "b.com", "b.com"}) // dup tolerated
	require.Equal(t, 2, m.Size())
}
