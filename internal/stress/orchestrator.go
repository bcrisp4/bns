package stress

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/tantalor93/dnspyre/v3/pkg/dnsbench"
	"github.com/tantalor93/dnspyre/v3/pkg/reporter"
)

const (
	defaultMockAddr   = "127.0.0.1:5355"
	readyProbeTimeout = 2 * time.Second
)

// AdminBaseURL returns "http://" + admin host:port.
func AdminBaseURL(adminHostPort string) string {
	return "http://" + adminHostPort
}

// WaitForReady polls <baseURL>/readyz every interval until it returns
// HTTP 200, or ctx is cancelled / deadline exceeded.
func WaitForReady(ctx context.Context, baseURL string, interval time.Duration) error {
	client := &http.Client{Timeout: readyProbeTimeout}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readyz not OK before deadline: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// WaitForDNSReady sends probe DNS queries to target (host:port) until
// consecutive successes confirm the server is actually handling traffic,
// or ctx is cancelled. This catches the brief window between /readyz
// returning 200 (HTTP listener up) and the DNS server being fully stable.
func WaitForDNSReady(ctx context.Context, target string, interval time.Duration, needed int) error {
	c := dns.NewClient()
	c.Transport.ReadTimeout = 1 * time.Second
	c.Transport.WriteTimeout = 1 * time.Second
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("parse target %q: %w", target, err)
	}
	probe := dns.NewMsg(host+".", dns.TypeA)

	t := time.NewTicker(interval)
	defer t.Stop()
	ok := 0
	for {
		_, _, err := c.Exchange(ctx, probe, "udp", target)
		if err == nil {
			ok++
			if ok >= needed {
				return nil
			}
		} else {
			ok = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("dns not ready before deadline: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// BNSEnv merges defaults with scenario overrides, returning a sorted
// slice of "KEY=value" strings ready for exec.Cmd.Env.
func BNSEnv(scenario, defaults map[string]string) []string {
	merged := map[string]string{}
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range scenario {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(out)
	return out
}

// Result is the outcome of a single Run.
type Result struct {
	OutDir           string
	TotalQueries     int64
	QPS              float64
	P50, P95, P99    time.Duration
	IOErrors         int64
	IDMismatches     int64
	Counters         map[string]int64
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64
}

// Run executes one stress run end-to-end. The returned Result mirrors
// the headline numbers in report.md, suitable for printing the one-line
// summary.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	scenario, ok := LookupScenario(cfg.Scenario)
	if !ok {
		return Result{}, fmt.Errorf("unknown scenario %q", cfg.Scenario)
	}

	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join("dist", "stress", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create out dir: %w", err)
	}

	bnsLog, err := os.Create(filepath.Join(cfg.OutDir, "bns.log"))
	if err != nil {
		return Result{}, err
	}
	defer bnsLog.Close()
	mockLog, err := os.Create(filepath.Join(cfg.OutDir, "mockupstream.log"))
	if err != nil {
		return Result{}, err
	}
	defer mockLog.Close()

	var bnsCmd, mockCmd *exec.Cmd
	if cfg.Spawn {
		mockCmd = exec.CommandContext(ctx, cfg.MockBin,
			"--listen.udp", defaultMockAddr,
			"--listen.tcp", defaultMockAddr,
		)
		mockCmd.Stdout = mockLog
		mockCmd.Stderr = mockLog
		if err := mockCmd.Start(); err != nil {
			return Result{}, fmt.Errorf("start mockupstream: %w", err)
		}
		defer terminate(mockCmd)

		bnsEnv := BNSEnv(scenario.BNSEnv, map[string]string{
			"BNS_LOGGING__LEVEL":              "info",
			"BNS_LOGGING__FORMAT":             "json",
			"BNS_LOGGING__QUERY_LOG__ENABLED": "false",
			"BNS_CACHE__CAPACITY":             "10000",
			"BNS_ADMIN__LISTEN":               cfg.Admin,
		})

		// Viper's AutomaticEnv does not handle slice indexing via env vars, so
		// blocklists.sources must be expressed in YAML. Write a temp config and
		// pass -c. Scalar overrides still flow through env or CLI flags.
		bnsConfigPath, err := writeBNSConfig(cfg.OutDir, cfg.BlocklistPath)
		if err != nil {
			return Result{}, fmt.Errorf("write bns config: %w", err)
		}

		bnsCmd = exec.CommandContext(ctx, cfg.BNSBin, "serve",
			"-c", bnsConfigPath,
			"--listen.udp", cfg.Target,
			"--listen.tcp", cfg.Target,
			"--upstream", defaultMockAddr,
			"--pprof",
		)
		bnsCmd.Env = append(os.Environ(), bnsEnv...)
		bnsCmd.Stdout = bnsLog
		bnsCmd.Stderr = bnsLog
		if err := bnsCmd.Start(); err != nil {
			return Result{}, fmt.Errorf("start bns: %w", err)
		}
		defer terminate(bnsCmd)
	}

	baseURL := AdminBaseURL(cfg.Admin)
	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	if err := WaitForReady(readyCtx, baseURL, 50*time.Millisecond); err != nil {
		return Result{}, fmt.Errorf("bns not ready: %w", err)
	}
	// HTTP /readyz is up, but wait for the DNS server to be stable before
	// benchmarking — a handful of consecutive successes eliminates startup jitter.
	dnsCtx, dnsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dnsCancel()
	if err := WaitForDNSReady(dnsCtx, cfg.Target, 20*time.Millisecond, 3); err != nil {
		return Result{}, fmt.Errorf("dns not ready: %w", err)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	before, beforeRaw, err := FetchSnapshot(httpClient, baseURL)
	if err != nil {
		return Result{}, fmt.Errorf("scrape before: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "before.prom"), beforeRaw, 0o644); err != nil {
		return Result{}, err
	}

	// Separate client for pprof: CPU profile blocks for cfg.PprofCPU before
	// returning headers, so it needs a timeout sized to that duration plus
	// transfer slack.
	pprofClient := &http.Client{Timeout: cfg.PprofCPU + 30*time.Second}

	pprofErr := make(chan error, 1)
	if cfg.PprofCPU > 0 {
		go func() {
			pprofErr <- capturePprof(ctx, pprofClient,
				fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", baseURL, int(cfg.PprofCPU.Seconds())),
				filepath.Join(cfg.OutDir, "cpu.pprof"))
		}()
	} else {
		pprofErr <- nil
	}

	start := time.Now()
	b := scenario.Build(cfg.Target, cfg.Duration, cfg.Concurrency)
	stdSilent(&b)
	if cfg.RateLimit > 0 {
		b.Rate = cfg.RateLimit
	}
	// @<path> entries are kingpin CLI sugar, not dnspyre library behaviour.
	// Library callers must expand them before Run; otherwise the literal
	// "@<path>" becomes a single hostname queried millions of times.
	if err := expandFileRefs(&b); err != nil {
		return Result{}, fmt.Errorf("expand queries: %w", err)
	}
	results, err := b.Run(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("dnspyre run: %w", err)
	}
	elapsed := time.Since(start)

	if err := <-pprofErr; err != nil {
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "cpu.pprof.err"), []byte(err.Error()), 0o644)
	}

	if cfg.PprofHeap {
		if err := capturePprof(ctx, pprofClient,
			baseURL+"/debug/pprof/heap",
			filepath.Join(cfg.OutDir, "heap.pprof")); err != nil {
			_ = os.WriteFile(filepath.Join(cfg.OutDir, "heap.pprof.err"), []byte(err.Error()), 0o644)
		}
	}

	after, afterRaw, err := FetchSnapshot(httpClient, baseURL)
	if err != nil {
		return Result{}, fmt.Errorf("scrape after: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "after.prom"), afterRaw, 0o644); err != nil {
		return Result{}, err
	}

	d := Diff(before, after)
	merged := reporter.Merge(&b, results)

	jsonPath := filepath.Join(cfg.OutDir, "dnspyre-results.json")
	if jsonFile, err := os.Create(jsonPath); err == nil {
		bArchival := b
		bArchival.JSON = true
		bArchival.Silent = false
		bArchival.Writer = jsonFile
		_ = reporter.PrintReport(&bArchival, results, start, elapsed)
		jsonFile.Close()
	}

	qps := float64(merged.Counters.Total) / elapsed.Seconds()

	res := Result{
		OutDir:           cfg.OutDir,
		TotalQueries:     merged.Counters.Total,
		QPS:              qps,
		P50:              time.Duration(merged.Hist.ValueAtQuantile(50)),
		P95:              time.Duration(merged.Hist.ValueAtQuantile(95)),
		P99:              time.Duration(merged.Hist.ValueAtQuantile(99)),
		IOErrors:         merged.Counters.IOError,
		IDMismatches:     merged.Counters.IDmismatch,
		Counters:         d.QueriesByOutcome,
		UpstreamQueries:  d.UpstreamQueries,
		CoalescedQueries: d.CoalescedQueries,
		CacheEvictions:   d.CacheEvictions,
		Panics:           d.Panics,
	}

	sha := gitSha()
	report := Render(ReportInput{
		Scenario:         cfg.Scenario,
		StartedAt:        start,
		Target:           cfg.Target,
		Admin:            cfg.Admin,
		BNSGitSha:        sha,
		GoVersion:        runtime.Version(),
		Host:             hostString(),
		Duration:         cfg.Duration,
		Concurrency:      cfg.Concurrency,
		TotalQueries:     res.TotalQueries,
		QPS:              res.QPS,
		P50:              res.P50,
		P95:              res.P95,
		P99:              res.P99,
		IOErrors:         res.IOErrors,
		IDMismatches:     res.IDMismatches,
		Counters:         res.Counters,
		UpstreamQueries:  res.UpstreamQueries,
		CoalescedQueries: res.CoalescedQueries,
		CacheEvictions:   res.CacheEvictions,
		Panics:           res.Panics,
		OutDir:           cfg.OutDir,
		PprofCPU:         "cpu.pprof",
		PprofHeap:        "heap.pprof",
	})
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.md"), []byte(report), 0o644); err != nil {
		return res, err
	}

	configJSON, err := json.MarshalIndent(map[string]any{
		"scenario":    cfg.Scenario,
		"target":      cfg.Target,
		"admin":       cfg.Admin,
		"duration":    cfg.Duration.String(),
		"concurrency": cfg.Concurrency,
		"git_sha":     sha,
		"go_version":  runtime.Version(),
	}, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "config.json"), configJSON, 0o644)
	}

	if res.IOErrors > cfg.MaxIOErrors || res.IDMismatches > 0 {
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "FAILED"),
			[]byte(fmt.Sprintf("io_errors=%d id_mismatches=%d", res.IOErrors, res.IDMismatches)),
			0o644)
		return res, fmt.Errorf("correctness failure: io_errors=%d id_mismatches=%d", res.IOErrors, res.IDMismatches)
	}

	return res, nil
}

// terminate sends SIGINT to cmd's process, waits up to 10s, then SIGKILL.
func terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// capturePprof streams a pprof endpoint to disk.
func capturePprof(ctx context.Context, c *http.Client, url, outPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof %s status %d", url, resp.StatusCode)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// writeBNSConfig generates a minimal YAML config file in outDir, returning
// its path. Viper does not handle slice indexing via env vars, so blocklist
// sources must be supplied via YAML. The file is named bns-config.yaml so
// it is archived alongside the other per-run artefacts.
// expandFileRefs replaces any `@<path>` entry in b.Queries with the lines
// of that file. The `@` prefix is kingpin v2 (dnspyre's CLI parser) sugar;
// when calling the dnspyre library directly we have to do the expansion
// ourselves. Non-@ entries pass through unchanged.
func expandFileRefs(b *dnsbench.Benchmark) error {
	out := make([]string, 0, len(b.Queries))
	for _, q := range b.Queries {
		if !strings.HasPrefix(q, "@") {
			out = append(out, q)
			continue
		}
		path := q[1:]
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return fmt.Errorf("read %s: %w", path, err)
		}
		f.Close()
	}
	b.Queries = out
	return nil
}

func writeBNSConfig(outDir, blocklistPath string) (string, error) {
	path := filepath.Join(outDir, "bns-config.yaml")
	var sb strings.Builder
	if blocklistPath != "" {
		sb.WriteString("blocklists:\n  sources:\n")
		fmt.Fprintf(&sb, "    - type: file\n      path: %q\n", blocklistPath)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func gitSha() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func hostString() string {
	out, err := exec.Command("uname", "-srm").Output()
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return strings.TrimSpace(string(out))
}
