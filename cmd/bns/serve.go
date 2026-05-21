package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

func newServeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the BNS DNS forwarder",
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load(config.LoadOptions{
				ConfigPath: cfgPath,
				FlagBinder: func(v *viper.Viper) error { return bindServeFlags(v, c) },
			})
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runServe(c.Context(), cfg)
		},
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", "", "Path to YAML config file")
	cmd.Flags().String("listen.udp", "", `UDP listen address host:port (default ":53")`)
	cmd.Flags().String("listen.tcp", "", `TCP listen address host:port (default ":53")`)
	cmd.Flags().Duration("listen.query-timeout", 0, "Per-query handling timeout (default 5s)")
	cmd.Flags().StringSlice("upstream", nil, "Upstream resolver addr host:port; repeat for multiple (no default; required)")
	cmd.Flags().StringSlice("blocklist", nil, "Blocklist file path; repeat for multiple (default none)")
	cmd.Flags().Int("cache.capacity", 0, "Maximum cached DNS responses (default 10000)")
	cmd.Flags().Duration("cache.min-ttl", 0, "Floor applied to cached entry TTL (default 0s)")
	cmd.Flags().Duration("cache.max-ttl", 0, "Ceiling applied to cached entry TTL (default 24h)")
	cmd.Flags().Duration("cache.negative-ttl-max", 0, "Ceiling applied to negative-cache TTL derived from SOA (default 15m)")
	cmd.Flags().String("admin.listen", "", `Admin HTTP listen address host:port for /metrics, /healthz, /readyz (default ":9090")`)
	cmd.Flags().Bool("pprof", false, "Expose /debug/pprof endpoints on the admin listener (default false)")
	cmd.Flags().String("logging.level", "", `Log level: debug|info|warn|error (default "info")`)
	cmd.Flags().String("logging.format", "", `Log format: json|text (default "json")`)
	cmd.Flags().Bool("logging.query-log", false, "Emit one JSON log line per DNS query (default false)")
	cmd.Flags().Duration("shutdown-timeout", 0, "Grace period for in-flight queries on shutdown (default 5s)")
	cmd.Flags().Duration("startup-probe-timeout", 0, "Deadline for the upstream warmup probe before marking ready (default 3s)")
	return cmd
}

func bindServeFlags(v *viper.Viper, c *cobra.Command) error {
	binds := []struct {
		viperKey string
		flagName string
	}{
		{"listen.udp", "listen.udp"},
		{"listen.tcp", "listen.tcp"},
		{"listen.query_timeout", "listen.query-timeout"},
		{"cache.capacity", "cache.capacity"},
		{"cache.min_ttl", "cache.min-ttl"},
		{"cache.max_ttl", "cache.max-ttl"},
		{"cache.negative_ttl_max", "cache.negative-ttl-max"},
		{"admin.listen", "admin.listen"},
		{"admin.pprof", "pprof"},
		{"logging.level", "logging.level"},
		{"logging.format", "logging.format"},
		{"logging.query_log.enabled", "logging.query-log"},
		{"shutdown_timeout", "shutdown-timeout"},
		{"startup_probe_timeout", "startup-probe-timeout"},
	}
	for _, b := range binds {
		if err := v.BindPFlag(b.viperKey, c.Flag(b.flagName)); err != nil {
			return err
		}
	}

	// Slice flags bypass BindPFlag — viper cannot expand a scalar StringSlice
	// flag into a nested []struct shape. Build the payload manually and v.Set,
	// which has higher precedence than YAML/env.
	setSliceFlag(v, c, "upstream", "upstreams", func(addr string) map[string]any {
		return map[string]any{"addr": addr, "timeout": "2s"}
	})
	setSliceFlag(v, c, "blocklist", "blocklists.sources", func(path string) map[string]any {
		// Name derived from the basename so the new schema's `name` requirement
		// is satisfied. This flag is going away in the http-source rewrite;
		// no need to support collision-free naming here.
		return map[string]any{"type": "file", "name": filepath.Base(path), "path": path}
	})
	return nil
}

func setSliceFlag(v *viper.Viper, c *cobra.Command, flagName, viperKey string, build func(string) map[string]any) {
	if !c.Flag(flagName).Changed {
		return
	}
	vals, _ := c.Flags().GetStringSlice(flagName)
	out := make([]map[string]any, 0, len(vals))
	for _, x := range vals {
		out = append(out, build(x))
	}
	v.Set(viperKey, out)
}

func runServe(ctx context.Context, cfg config.Config) error {
	logger := logging.New(cfg.Logging, os.Stdout)
	logger.Info("starting BNS",
		"udp", cfg.Listen.UDP, "tcp", cfg.Listen.TCP,
		"admin", cfg.Admin.Listen, "upstreams", upstreamAddrs(cfg.Upstreams))

	reg := prometheus.NewRegistry()
	mtr := metrics.New(reg)
	rdy := health.NewReadiness()
	queryLog := logging.QueryLogger(cfg.Logging.QueryLog, os.Stdout)

	store := blocklist.NewCacheStore(cfg.Blocklists.CacheDir)
	sources := make([]blocklist.Source, 0, len(cfg.Blocklists.Sources))
	for _, s := range cfg.Blocklists.Sources {
		switch s.Type {
		case "file":
			sources = append(sources, blocklist.FileSource{Path: s.Path})
		case "http":
			sources = append(sources, blocklist.NewHTTPSource(s.Name, s.URL, store))
		default:
			return fmt.Errorf("blocklist source %q: unsupported type %q", s.Name, s.Type)
		}
	}
	_ = store
	loader := blocklist.NewLoader(sources)
	initial, count, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load blocklist: %w", err)
	}
	holder := blocklist.NewHolder(initial)
	mtr.BlocklistEntries.Set(float64(initial.Size()))
	mtr.BlocklistLoadedTimestamp.Set(float64(time.Now().Unix()))
	switch {
	case len(cfg.Blocklists.Sources) == 0:
		logger.Warn("no blocklist sources configured — block stage is a no-op",
			"raw", count, "unique", initial.Size())
	case initial.Size() == 0:
		logger.Warn("blocklist sources configured but yielded zero entries — check paths and file contents",
			"sources", len(cfg.Blocklists.Sources), "raw", count, "unique", initial.Size())
	default:
		logger.Info("blocklist loaded",
			"sources", len(cfg.Blocklists.Sources), "raw", count, "unique", initial.Size())
	}
	rdy.SetBlocklistReady(true)

	ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
	names := make([]string, 0, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		ups = append(ups, upstream.NewUDPClient(u.Addr, u.Timeout))
		names = append(names, u.Addr)
	}
	pool := upstream.NewPool(ups, names, mtr)

	lru := cache.NewLRU(cfg.Cache.Capacity)
	lru.SetObserver(mtr.CacheObserver())
	mtr.CacheCapacity.Set(float64(cfg.Cache.Capacity))

	chainResolver := chain.Build(chain.Deps{
		Upstream:  pool,
		Cache:     lru,
		CacheCfg:  cfg.Cache,
		Blocklist: holder,
		QueryLog:  queryLog,
		Metrics:   mtr,
	})
	handler := resolver.NewHandler(chainResolver)

	udpConn, err := net.ListenPacket("udp", cfg.Listen.UDP)
	if err != nil {
		return fmt.Errorf("bind udp %s: %w", cfg.Listen.UDP, err)
	}
	tcpLn, err := net.Listen("tcp", cfg.Listen.TCP)
	if err != nil {
		return fmt.Errorf("bind tcp %s: %w", cfg.Listen.TCP, err)
	}
	dnsSrv := server.New(udpConn, tcpLn, handler)

	adminLn, err := net.Listen("tcp", cfg.Admin.Listen)
	if err != nil {
		return fmt.Errorf("bind admin %s: %w", cfg.Admin.Listen, err)
	}
	var adminOpts []admin.Option
	if cfg.Admin.Pprof {
		adminOpts = append(adminOpts, admin.WithPprof())
	}
	adminSrv := admin.New(adminLn, reg, rdy, adminOpts...)

	if err := warmupProbe(ctx, pool, cfg.StartupProbeTimeout); err != nil {
		logger.Warn("startup upstream probe failed", "err", err)
	} else {
		rdy.SetUpstreamReady(true)
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return dnsSrv.Serve() })
	// Wait for both listeners to be actually serving before flipping the
	// readiness flag (and before any shutdown could race with init).
	g.Go(func() error {
		readyCtx, cancel := context.WithTimeout(gctx, cfg.StartupProbeTimeout)
		defer cancel()
		if err := dnsSrv.Ready(readyCtx); err != nil {
			return fmt.Errorf("dns listeners not ready: %w", err)
		}
		rdy.SetListenersReady(true)
		return nil
	})
	g.Go(func() error {
		if err := adminSrv.Serve(); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	g.Go(func() error {
		// SIGHUP triggers a live blocklist reload without restarting the process.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		defer signal.Stop(ch)
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-ch:
				logger.Info("SIGHUP received, reloading blocklist")
				rctx, cancel := context.WithTimeout(gctx, 30*time.Second)
				next, rcount, rerr := loader.Load(rctx)
				cancel()
				if rerr != nil {
					logger.Error("blocklist reload failed", "err", rerr)
					mtr.BlocklistReloadsTotal.WithLabelValues("error").Inc()
					continue
				}
				holder.Swap(next)
				mtr.BlocklistEntries.Set(float64(next.Size()))
				mtr.BlocklistLoadedTimestamp.Set(float64(time.Now().Unix()))
				mtr.BlocklistReloadsTotal.WithLabelValues("ok").Inc()
				logger.Info("blocklist reloaded", "raw", rcount, "unique", next.Size())
			}
		}
	})
	g.Go(func() error {
		<-gctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = adminSrv.Shutdown(shCtx)
		_ = dnsSrv.Shutdown(shCtx)
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// warmupProbe sends a single A query for a.root-servers.net. to verify
// that at least one upstream is reachable before marking readiness.
func warmupProbe(ctx context.Context, p upstream.Upstream, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := dns.NewMsg("a.root-servers.net.", dns.TypeA)
	_, err := p.Exchange(ctx, req)
	return err
}

func upstreamAddrs(ups []config.Upstream) []string {
	out := make([]string, len(ups))
	for i, u := range ups {
		out[i] = u.Addr
	}
	return out
}
