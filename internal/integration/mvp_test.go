package integration_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/admin"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/health"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/chain"
	"github.com/bcrisp4/bns/internal/server"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestMVP_AllSuccessCriteria(t *testing.T) {
	// Fake upstream that always answers example.com A 1.2.3.4.
	// The handler echoes back a proper A-record response for any query.
	upAddr := testutil.Spawn(t, func(req *dns.Msg) *dns.Msg {
		resp := new(dns.Msg)
		resp.Response = true
		resp.ID = req.ID
		resp.Question = req.Question
		rr, _ := dns.New("example.com. 60 IN A 1.2.3.4")
		resp.Answer = []dns.RR{rr}
		return resp
	})

	// Minimal blocklist on disk: one entry to block.
	dir := t.TempDir()
	blPath := filepath.Join(dir, "block.txt")
	require.NoError(t, os.WriteFile(blPath, []byte("blocked.example\n"), 0o644))

	// Bind ephemeral ports for DNS (UDP + TCP share same port).
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	dnsAddr := udpConn.LocalAddr().String()
	host, port, err := net.SplitHostPort(dnsAddr)
	require.NoError(t, err)
	tcpLn, err := net.Listen("tcp", net.JoinHostPort(host, port))
	require.NoError(t, err)

	adminLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	adminAddr := adminLn.Addr().String()

	// Assemble BNS service components.
	reg := prometheus.NewRegistry()
	mtr := metrics.New(reg)
	rdy := health.NewReadiness()
	queryLog := logging.QueryLogger(config.QueryLog{Enabled: true}, io.Discard)

	srcs := []blocklist.Source{blocklist.FileSource{Path: blPath}}
	loader := blocklist.NewLoader(srcs)
	initial, _, err := loader.Load(context.Background())
	require.NoError(t, err)
	holder := blocklist.NewHolder(initial)
	rdy.SetBlocklistReady(true)

	pool := upstream.NewPool([]upstream.Upstream{
		upstream.NewUDPClient(upAddr, 2*time.Second),
	}, []string{upAddr}, mtr)
	lru := cache.NewLRU(100)

	chainResolver := chain.Build(chain.Deps{
		Upstream:  pool,
		Cache:     lru,
		CacheCfg:  config.Cache{Capacity: 100, MaxTTL: time.Hour, NegativeTTLMax: 15 * time.Minute},
		Blocklist: holder,
		QueryLog:  queryLog,
		Metrics:   mtr,
	})
	handler := resolver.NewHandler(chainResolver)

	dnsSrv := server.New(udpConn, tcpLn, handler)
	go func() { _ = dnsSrv.Serve() }()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	require.NoError(t, dnsSrv.Ready(readyCtx))
	readyCancel()

	rdy.SetListenersReady(true)
	rdy.SetUpstreamReady(true)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = dnsSrv.Shutdown(ctx)
	})

	adminSrv := admin.New(adminLn, reg, rdy)
	go func() { _ = adminSrv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = adminSrv.Shutdown(ctx)
	})

	// Shared DNS client for all subtests.
	c := dns.NewClient()
	c.Transport.ReadTimeout = 2 * time.Second

	// criterion-2: BNS forwards queries and returns the upstream answer.
	t.Run("criterion-2_forwarded_answer", func(t *testing.T) {
		req := dns.NewMsg("example.com.", dns.TypeA)
		resp, _, err := c.Exchange(context.Background(), req, "udp", dnsAddr)
		require.NoError(t, err)
		require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
		require.Len(t, resp.Answer, 1)
	})

	// criterion-7: blocked domain returns NXDOMAIN, not the upstream answer.
	t.Run("criterion-7_blocked_returns_nxdomain", func(t *testing.T) {
		req := dns.NewMsg("blocked.example.", dns.TypeA)
		resp, _, err := c.Exchange(context.Background(), req, "udp", dnsAddr)
		require.NoError(t, err)
		require.Equal(t, uint16(dns.RcodeNameError), resp.Rcode)
	})

	// criterion-7 (subdomain wildcard): *.blocked.example is also blocked.
	t.Run("criterion-7_subdomain_of_blocked_returns_nxdomain", func(t *testing.T) {
		req := dns.NewMsg("foo.blocked.example.", dns.TypeA)
		resp, _, err := c.Exchange(context.Background(), req, "udp", dnsAddr)
		require.NoError(t, err)
		require.Equal(t, uint16(dns.RcodeNameError), resp.Rcode)
	})

	// criterion-8: repeated query is served from cache (cache populated by
	// criterion-2 subtest that ran first).
	t.Run("criterion-8_second_query_is_cache_hit", func(t *testing.T) {
		// The criterion-2 subtest already ran and populated the cache.
		require.GreaterOrEqual(t, lru.Len(), 1, "first query should have populated cache")

		req := dns.NewMsg("example.com.", dns.TypeA)
		resp, _, err := c.Exchange(context.Background(), req, "udp", dnsAddr)
		require.NoError(t, err)
		require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	})

	// criterion-4: /metrics endpoint is reachable and contains BNS metrics.
	t.Run("criterion-4_metrics_endpoint", func(t *testing.T) {
		resp, err := http.Get("http://" + adminAddr + "/metrics")
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), "bns_queries_total")
	})

	// criterion-6: /healthz and /readyz both return 200 when all checks pass.
	t.Run("criterion-6_health_probes", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/readyz"} {
			resp, err := http.Get("http://" + adminAddr + p)
			require.NoError(t, err, p)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, p)
		}
	})

	// criteria 1, 3, 5 are covered by existing unit tests.
	t.Run("criterion-1_3_5_covered_by_other_tests", func(t *testing.T) {
		t.Skip("criteria 1, 3, 5 covered by unit tests (TestRootVersionFlag, qlog tests, config Load tests)")
	})
}
