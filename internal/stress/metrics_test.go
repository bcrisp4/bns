package stress_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func TestSnapshot_Parse(t *testing.T) {
	snap, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)

	require.Equal(t, int64(100), snap.QueriesByOutcome["forwarded"])
	require.Equal(t, int64(10), snap.QueriesByOutcome["blocked"])
	require.Equal(t, int64(95), snap.UpstreamQueries)
	require.Equal(t, int64(5), snap.CoalescedQueries)
	require.Equal(t, int64(0), snap.CacheEvictions)
	require.Equal(t, int64(0), snap.Panics)
	require.Equal(t, int64(112), snap.DurationHist.Count)
}

func TestSnapshot_Diff(t *testing.T) {
	before, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)
	after, err := stress.ParseSnapshot(readFixture(t, "after.prom"))
	require.NoError(t, err)

	d := stress.Diff(before, after)

	require.Equal(t, int64(1000), d.QueriesByOutcome["forwarded"])
	require.Equal(t, int64(200), d.QueriesByOutcome["blocked"])
	require.Equal(t, int64(10), d.QueriesByOutcome["nxdomain"])
	require.Equal(t, int64(1), d.QueriesByOutcome["error"])
	require.Equal(t, int64(1211), d.TotalQueries)
	require.Equal(t, int64(300), d.UpstreamQueries)
	require.Equal(t, int64(50), d.CoalescedQueries)
	require.Equal(t, int64(3), d.CacheEvictions)
	require.Equal(t, int64(0), d.Panics)

	require.InDelta(t, 0.7522, d.CacheHitRate(), 0.001)

	p50 := d.DurationQuantile(0.50)
	require.GreaterOrEqual(t, p50.Seconds(), 0.0)
	require.LessOrEqual(t, p50.Seconds(), 0.0005)

	p99 := d.DurationQuantile(0.99)
	require.GreaterOrEqual(t, p99.Seconds(), 0.0005)
}

func TestSnapshot_DiffZeroAcrossEqualSnapshots(t *testing.T) {
	snap, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)
	d := stress.Diff(snap, snap)
	require.Equal(t, int64(0), d.TotalQueries)
	require.Equal(t, int64(0), d.UpstreamQueries)
}
