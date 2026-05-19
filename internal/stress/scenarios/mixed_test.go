package scenarios_test

import (
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/bcrisp4/bns/internal/stress/scenarios"
	"github.com/stretchr/testify/require"
)

func TestMixed_BuildPopulatesBenchmark(t *testing.T) {
	s := scenarios.NewMixed()
	require.Equal(t, "mixed", s.Name)

	b := s.Build("127.0.0.1:5354", 30*time.Second, 25)

	require.Equal(t, "127.0.0.1:5354", b.Server)
	require.Equal(t, []string{"A", "AAAA"}, b.Types)
	require.Equal(t, uint32(25), b.Concurrency)
	require.Equal(t, 30*time.Second, b.Duration)
	require.InDelta(t, 0.7, b.Probability, 0.0001)
	require.Equal(t, []string{"@scripts/stress/queries/mixed.txt"}, b.Queries)
	require.True(t, b.Recurse)
	require.True(t, b.Silent)
	require.False(t, b.ProgressBar)
	require.False(t, b.JSON)
	require.True(t, b.Rcodes)
}

func TestRegistry_LookupMixed(t *testing.T) {
	got, ok := stress.LookupScenario("mixed")
	require.True(t, ok)
	require.Equal(t, "mixed", got.Name)
}

func TestRegistry_LookupUnknownReturnsFalse(t *testing.T) {
	_, ok := stress.LookupScenario("does-not-exist")
	require.False(t, ok)
}
