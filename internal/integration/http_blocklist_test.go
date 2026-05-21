package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver/chain"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestHTTPBlocklistSource_FetchThenBlock asserts that a cold-start BNS
// with only an HTTP source: (1) initially does not block (cache empty),
// (2) fetches the body from the test server, (3) after the fetcher
// triggers reload, blocks the listed domain via NXDOMAIN.
func TestHTTPBlocklistSource_FetchThenBlock(t *testing.T) {
	// Test HTTP server serving a single blocked domain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("blocked.example\n"))
	}))
	t.Cleanup(srv.Close)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))

	// Synthetic upstream answering A queries with 1.2.3.4.
	upstreamAddr := testutil.Spawn(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg)
		resp.Response = true
		resp.ID = req.ID
		resp.Question = req.Question
		rr, _ := dns.New("blocked.example. 60 IN A 1.2.3.4")
		resp.Answer = []dns.RR{rr}
		return resp
	})

	reg := prometheus.NewRegistry()
	mtr := metrics.New(reg)

	store := blocklist.NewCacheStore(cacheDir)
	httpSource := blocklist.NewHTTPSource("test", srv.URL, store)
	loader := blocklist.NewLoader([]blocklist.Source{httpSource})
	initial, _, err := loader.Load(context.Background())
	require.NoError(t, err)
	holder := blocklist.NewHolder(initial)

	pool := upstream.NewPool(
		[]upstream.Upstream{upstream.NewUDPClient(upstreamAddr, time.Second)},
		[]string{upstreamAddr},
		mtr,
	)

	chainResolver := chain.Build(chain.Deps{
		Upstream:  pool,
		Cache:     cache.NewLRU(100),
		CacheCfg:  config.Cache{MaxTTL: time.Hour, NegativeTTLMax: time.Minute},
		Blocklist: holder,
		QueryLog:  logging.QueryLogger(config.QueryLog{}, os.Stderr),
		Metrics:   mtr,
	})

	// Before fetch: blocked.example is NOT blocked (cache empty → matcher empty).
	res, err := chainResolver.Resolve(context.Background(), dns.NewMsg("blocked.example.", dns.TypeA))
	require.NoError(t, err)
	require.NotEqual(t, uint16(dns.RcodeNameError), res.Rcode)

	// Run fetcher; onReload swaps the matcher with the freshly-fetched body.
	logger := logging.New(config.Logging{Level: "info", Format: "text"}, os.Stderr)
	fetcher := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store:    store,
		Client:   srv.Client(),
		Interval: time.Hour,
		Logger:   logger,
		Metrics:  mtr.BlocklistFetcherMetrics(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		_ = fetcher.Run(ctx, []blocklist.FetchTarget{{Name: "test", URL: srv.URL}}, func() {
			next, _, _ := loader.Load(context.Background())
			holder.Swap(next)
		})
		close(done)
	}()

	// Eventually the domain is blocked (NXDOMAIN).
	require.Eventually(t, func() bool {
		res, err := chainResolver.Resolve(context.Background(), dns.NewMsg("blocked.example.", dns.TypeA))
		return err == nil && res.Rcode == uint16(dns.RcodeNameError)
	}, 3*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}
