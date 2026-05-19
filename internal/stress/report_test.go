package stress_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func TestReport_RenderGolden(t *testing.T) {
	input := stress.ReportInput{
		Scenario:     "mixed",
		StartedAt:    time.Date(2026, 5, 19, 14, 22, 3, 0, time.UTC),
		Target:       "127.0.0.1:5354",
		Admin:        "127.0.0.1:9090",
		BNSGitSha:    "abc1234",
		GoVersion:    "go1.26.3",
		Host:         "linux/amd64 6.17.0-23-generic",
		Duration:     60 * time.Second,
		Concurrency:  50,
		TotalQueries: 1211,
		QPS:          20.18,
		P50:          400 * time.Microsecond,
		P95:          1100 * time.Microsecond,
		P99:          1800 * time.Microsecond,
		IOErrors:     0,
		IDMismatches: 0,
		Counters: map[string]int64{
			"forwarded": 1000,
			"blocked":   200,
			"nxdomain":  10,
			"error":     1,
		},
		UpstreamQueries:  300,
		CoalescedQueries: 50,
		CacheEvictions:   3,
		Panics:           0,
		PprofCPU:         "cpu.pprof",
		PprofHeap:        "heap.pprof",
	}

	got := stress.Render(input)

	wantBytes, err := os.ReadFile(filepath.Join("testdata", "report_golden.md"))
	require.NoError(t, err)
	require.Equal(t, string(wantBytes), got)
}
