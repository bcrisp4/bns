package metrics_test

import (
	"testing"

	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestNew_RegistersExpectedCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	require.NotNil(t, m)

	m.QueriesTotal.WithLabelValues("forwarded", "A").Inc()
	m.QueriesTotal.WithLabelValues("blocked", "A").Inc()

	fams, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(fams))
	for _, f := range fams {
		names[f.GetName()] = true
	}
	require.True(t, names["bns_queries_total"], "queries counter must be registered")
	require.True(t, names["bns_query_duration_seconds"], "query duration histogram must be registered")
	require.True(t, names["bns_cache_entries"], "cache entries gauge must be registered")
}
