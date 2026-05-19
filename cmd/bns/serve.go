package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	cmd.Flags().String("listen.udp", "", "UDP listen address (overrides config)")
	cmd.Flags().String("listen.tcp", "", "TCP listen address (overrides config)")
	cmd.Flags().StringSlice("upstream", nil, "Upstream addr (repeatable)")
	return cmd
}

func bindServeFlags(v *viper.Viper, c *cobra.Command) error {
	if err := v.BindPFlag("listen.udp", c.Flag("listen.udp")); err != nil {
		return err
	}
	if err := v.BindPFlag("listen.tcp", c.Flag("listen.tcp")); err != nil {
		return err
	}
	if c.Flag("upstream").Changed {
		ups, _ := c.Flags().GetStringSlice("upstream")
		out := make([]map[string]any, 0, len(ups))
		for _, addr := range ups {
			out = append(out, map[string]any{"addr": addr, "timeout": "2s"})
		}
		v.Set("upstreams", out)
	}
	return nil
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

	sources := make([]blocklist.Source, 0, len(cfg.Blocklists.Sources))
	for _, s := range cfg.Blocklists.Sources {
		if s.Type != "file" {
			return fmt.Errorf("blocklist source type %q is not supported in MVP", s.Type)
		}
		sources = append(sources, blocklist.FileSource{Path: s.Path})
	}
	loader := blocklist.NewLoader(sources)
	initial, count, err := loader.Load(ctx)
	if err != nil {
		return fmt.Errorf("load blocklist: %w", err)
	}
	holder := blocklist.NewHolder(initial)
	mtr.BlocklistEntries.Set(float64(initial.Size()))
	mtr.BlocklistLoadedTimestamp.Set(float64(time.Now().Unix()))
	logger.Info("blocklist loaded", "raw", count, "unique", initial.Size())
	rdy.SetBlocklistReady(true)

	ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		ups = append(ups, upstream.NewUDPClient(u.Addr, u.Timeout))
	}
	pool := upstream.NewPool(ups)

	lru := cache.NewLRU(cfg.Cache.Capacity)
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
	adminSrv := admin.New(adminLn, reg, rdy)

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
