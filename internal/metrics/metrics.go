// Package metrics owns the Prometheus collectors used throughout BNS.
// One Metrics instance is created at startup and threaded through
// constructors that need to record values.
package metrics

import (
	"time"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics is the bundle of all BNS collectors.
//
// Cardinality discipline:
//   - "outcome" ∈ {blocked, nxdomain, forwarded, error} (4 values)
//   - "qtype" is restricted to a small allowlist via NormalizeQType (~10)
//   - "upstream" cardinality = configured upstream count (2-3 typical)
//   - NEVER add a qname label
type Metrics struct {
	QueriesTotal             *prometheus.CounterVec
	QueryDurationSeconds     *prometheus.HistogramVec
	UpstreamQueriesTotal     *prometheus.CounterVec
	UpstreamDurationSeconds  *prometheus.HistogramVec
	CacheEntries             prometheus.Gauge
	CacheCapacity            prometheus.Gauge
	CacheEvictionsTotal      prometheus.Counter
	BlocklistEntries         prometheus.Gauge
	BlocklistLoadedTimestamp prometheus.Gauge
	BlocklistReloadsTotal    *prometheus.CounterVec
	CoalescedQueriesTotal         prometheus.Counter
	PanicsTotal                   prometheus.Counter
	BlocklistFetchTotal           *prometheus.CounterVec
	BlocklistLastSuccessTimestamp *prometheus.GaugeVec
	BlocklistEntriesBySource      *prometheus.GaugeVec
}

// New constructs the bundle, registers every collector with reg, and
// returns the bundle. Also registers default Go/process collectors so
// runtime stats appear on /metrics.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		QueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bns_queries_total",
			Help: "Total DNS queries handled, by outcome and qtype.",
		}, []string{"outcome", "qtype"}),
		QueryDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bns_query_duration_seconds",
			Help:    "End-to-end query duration in seconds, by outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),
		UpstreamQueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bns_upstream_queries_total",
			Help: "Total upstream queries, by upstream and outcome.",
		}, []string{"upstream", "outcome"}),
		UpstreamDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bns_upstream_duration_seconds",
			Help:    "Upstream exchange duration in seconds, by upstream.",
			Buckets: prometheus.DefBuckets,
		}, []string{"upstream"}),
		CacheEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bns_cache_entries", Help: "Current number of entries in the cache.",
		}),
		CacheCapacity: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bns_cache_capacity", Help: "Configured maximum cache entries.",
		}),
		CacheEvictionsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bns_cache_evictions_total", Help: "Cache evictions (LRU pressure).",
		}),
		BlocklistEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bns_blocklist_entries", Help: "Distinct FQDNs in the active blocklist.",
		}),
		BlocklistLoadedTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bns_blocklist_loaded_timestamp_seconds",
			Help: "Unix time of last successful blocklist load.",
		}),
		BlocklistReloadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bns_blocklist_reloads_total", Help: "Blocklist reloads, by outcome.",
		}, []string{"outcome"}),
		CoalescedQueriesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bns_coalesced_queries_total",
			Help: "Concurrent identical queries collapsed onto an existing in-flight call.",
		}),
		PanicsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bns_panics_total",
			Help: "Recovered panics anywhere in the resolver chain.",
		}),
		BlocklistFetchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bns_blocklist_fetch_total",
			Help: "HTTP blocklist fetches, by source and outcome (success|not_modified|failure).",
		}, []string{"source", "outcome"}),
		BlocklistLastSuccessTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bns_blocklist_last_success_timestamp_seconds",
			Help: "Unix time of the last successful fetch (success OR not_modified) per source.",
		}, []string{"source"}),
		BlocklistEntriesBySource: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "bns_blocklist_entries_by_source",
			Help: "Parsed entry count per source after the most recent successful fetch.",
		}, []string{"source"}),
	}

	reg.MustRegister(
		m.QueriesTotal, m.QueryDurationSeconds,
		m.UpstreamQueriesTotal, m.UpstreamDurationSeconds,
		m.CacheEntries, m.CacheCapacity, m.CacheEvictionsTotal,
		m.BlocklistEntries, m.BlocklistLoadedTimestamp, m.BlocklistReloadsTotal,
		m.CoalescedQueriesTotal, m.PanicsTotal,
		m.BlocklistFetchTotal, m.BlocklistLastSuccessTimestamp, m.BlocklistEntriesBySource,
	)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	// Pre-initialize vec metrics with the outcome values that are actually
	// emitted so they appear in /metrics output even before any queries are
	// processed. Without this, Gather() omits HistogramVec/CounterVec entries
	// that have never been observed, which would make dashboards and alerts
	// unreliable at startup. "hit" and "miss" are omitted — the resolver chain
	// does not distinguish cache hits from forwarded responses at the outer
	// metric stage.
	for _, outcome := range []string{"blocked", "error", "forwarded", "nxdomain"} {
		m.QueryDurationSeconds.WithLabelValues(outcome)
	}

	return m
}

// BlocklistFetcherMetrics returns the adapter wiring the Fetcher's
// metric hooks into this Metrics bundle.
func (m *Metrics) BlocklistFetcherMetrics() blocklist.FetcherMetrics {
	return blocklist.FetcherMetrics{
		IncFetch: func(source string, outcome blocklist.FetchOutcome) {
			m.BlocklistFetchTotal.WithLabelValues(source, string(outcome)).Inc()
		},
		SetLastSuccess: func(source string, ts time.Time) {
			m.BlocklistLastSuccessTimestamp.WithLabelValues(source).Set(float64(ts.Unix()))
		},
		SetEntries: func(source string, n int) {
			m.BlocklistEntriesBySource.WithLabelValues(source).Set(float64(n))
		},
	}
}

// CacheObserver returns an object satisfying cache.MetricsObserver
// (defined in internal/cache) for this Metrics bundle. The cache package
// uses a local interface to avoid an import cycle.
func (m *Metrics) CacheObserver() *cacheObserver {
	return &cacheObserver{m: m}
}

type cacheObserver struct{ m *Metrics }

func (o *cacheObserver) SetEntries(n int) { o.m.CacheEntries.Set(float64(n)) }
func (o *cacheObserver) IncEvictions()    { o.m.CacheEvictionsTotal.Inc() }

// NormalizeQType collapses an uncommon DNS qtype into "other" so the
// {qtype=} label can never grow without bound.
func NormalizeQType(qtype string) string {
	switch qtype {
	case "A", "AAAA", "CNAME", "PTR", "MX", "TXT", "NS", "SOA", "SRV":
		return qtype
	default:
		return "other"
	}
}
