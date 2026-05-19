// Command bns-stress runs a BNS stress test. See
// docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bcrisp4/bns/internal/stress"
	_ "github.com/bcrisp4/bns/internal/stress/scenarios" // register scenarios
)

func main() {
	cfg := stress.Defaults()
	flag.StringVar(&cfg.Scenario, "scenario", cfg.Scenario, "Scenario name")
	flag.StringVar(&cfg.Target, "target", cfg.Target, "BNS DNS listener (host:port)")
	flag.StringVar(&cfg.Admin, "admin", cfg.Admin, "BNS admin endpoint (host:port)")
	flag.BoolVar(&cfg.Spawn, "spawn", cfg.Spawn, "Spawn a local bns + mockupstream (set --spawn=false to point at remote)")
	flag.DurationVar(&cfg.Duration, "duration", cfg.Duration, "Load generation duration")
	concurrency := flag.Uint("concurrency", uint(cfg.Concurrency), "dnspyre worker count")
	flag.IntVar(&cfg.RateLimit, "rate-limit", cfg.RateLimit, "Global queries/sec rate limit (0 = unbounded)")
	flag.StringVar(&cfg.BNSBin, "bns-bin", cfg.BNSBin, "Path to bns binary")
	flag.StringVar(&cfg.MockBin, "mock-bin", cfg.MockBin, "Path to mockupstream binary")
	flag.StringVar(&cfg.BlocklistPath, "blocklist", cfg.BlocklistPath, "Path to blocklist file (empty = bns default)")
	flag.StringVar(&cfg.OutDir, "out", cfg.OutDir, "Per-run output dir (empty = dist/stress/<ts>/)")
	flag.DurationVar(&cfg.PprofCPU, "pprof-cpu", cfg.PprofCPU, "CPU profile duration (0 = disabled)")
	flag.BoolVar(&cfg.PprofHeap, "pprof-heap", cfg.PprofHeap, "Capture heap profile at end of run")
	flag.Parse()
	cfg.Concurrency = uint32(*concurrency)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	res, err := stress.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bns-stress failed: %v (report=%s)\n", err, res.OutDir)
		os.Exit(1)
	}

	fmt.Printf("%s %s c=%d QPS=%.0f p99=%.1fms errors=%d blocked=%.1f%% report=%s\n",
		cfg.Scenario,
		cfg.Duration,
		cfg.Concurrency,
		res.QPS,
		float64(res.P99)/float64(1_000_000),
		res.IOErrors,
		blockedPct(res),
		res.OutDir,
	)
}

func blockedPct(r stress.Result) float64 {
	if r.TotalQueries == 0 {
		return 0
	}
	return 100.0 * float64(r.Counters["blocked"]) / float64(r.TotalQueries)
}
