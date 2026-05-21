package blocklist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	defaultFetchTimeout = 60 * time.Second
	maxBodyBytes        = 64 * 1024 * 1024 // 64 MiB
)

// FetchOutcome enumerates the result classes of a single HTTP fetch.
// String values double as the {outcome=...} Prometheus label.
type FetchOutcome string

const (
	FetchOutcomeSuccess     FetchOutcome = "success"
	FetchOutcomeNotModified FetchOutcome = "not_modified"
	FetchOutcomeFailure     FetchOutcome = "failure"
)

// FetchTarget identifies one HTTP source to fetch.
type FetchTarget struct {
	Name string
	URL  string
}

// FetchResult is the per-target outcome returned by FetchOne.
type FetchResult struct {
	Outcome    FetchOutcome
	Bytes      int           // body bytes written; 0 for not_modified / failure
	Entries    int           // parsed entries written; 0 unless success
	Duration   time.Duration // wall-clock for the fetch
	StatusCode int           // last HTTP status seen; 0 on transport error
	Err        error         // populated on failure for caller logging
}

// FetcherConfig holds dependencies + tunables for a Fetcher.
type FetcherConfig struct {
	Store     *CacheStore
	Client    *http.Client
	Interval  time.Duration  // global refresh interval; <=0 disables ticker
	Logger    *slog.Logger   // optional; nil → discard
	Metrics   FetcherMetrics // optional; zero value → no-op
	UserAgent string         // optional; empty → default "bns/dev"
}

// FetcherMetrics decouples the fetcher from the concrete metrics
// package. internal/metrics provides an adapter.
type FetcherMetrics struct {
	IncFetch       func(source string, outcome FetchOutcome)
	SetLastSuccess func(source string, ts time.Time)
	SetEntries     func(source string, n int)
}

// Fetcher owns the HTTP client + ticker that keeps the on-disk cache
// fresh. FetchOne is the smallest unit; Run drives the ticker loop.
//
// Concurrency: FetchOne is NOT safe to call concurrently on the same
// target — concurrent Writes to the same URL race on the shared tmp
// path inside CacheStore. The Run loop calls FetchOne serially per
// target by design; other callers must serialise too.
type Fetcher struct {
	cfg        FetcherConfig
	refreshNow chan struct{}
}

// NewFetcher constructs a Fetcher. Client is the only required
// dependency; tests typically pass httptest.NewServer.Client().
func NewFetcher(cfg FetcherConfig) *Fetcher {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: defaultFetchTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "bns/dev (+https://github.com/bcrisp4/bns)"
	}
	return &Fetcher{
		cfg:        cfg,
		refreshNow: make(chan struct{}, refreshChanBuf),
	}
}

// FetchOne performs a single conditional GET and persists the result.
// Returns nil error for every outcome; consult res.Outcome and res.Err.
func (f *Fetcher) FetchOne(ctx context.Context, t FetchTarget) (FetchResult, error) {
	start := time.Now()
	res := FetchResult{Outcome: FetchOutcomeFailure}

	_, prevMeta, _ := f.cfg.Store.Read(t.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		res.Err = fmt.Errorf("build request: %w", err)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept-Encoding", "gzip")
	if prevMeta.ETag != "" {
		req.Header.Set("If-None-Match", prevMeta.ETag)
	}
	if prevMeta.LastModified != "" {
		req.Header.Set("If-Modified-Since", prevMeta.LastModified)
	}

	resp, err := f.cfg.Client.Do(req)
	if err != nil {
		res.Err = fmt.Errorf("http do: %w", err)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}
	defer resp.Body.Close()
	res.StatusCode = resp.StatusCode

	switch resp.StatusCode {
	case http.StatusNotModified:
		res.Outcome = FetchOutcomeNotModified
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		f.markSuccess(t.Name, start)
		return res, nil
	case http.StatusOK:
		// fall through
	default:
		res.Err = fmt.Errorf("unexpected status %d", resp.StatusCode)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}

	limited := io.LimitReader(resp.Body, maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		res.Err = fmt.Errorf("read body: %w", err)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}
	if len(body) > maxBodyBytes {
		res.Err = fmt.Errorf("body exceeds %d bytes", maxBodyBytes)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}

	entries := parseBody(body)
	if len(body) > 0 && len(entries) == 0 {
		// Heuristic: a non-empty body that parses to zero entries is
		// almost certainly an HTML error page or garbage response (real
		// blocklists with zero useful FQDNs don't exist in practice).
		// Refuse to overwrite a presumably-good cache with garbage.
		res.Err = errors.New("body parsed to zero entries; refusing to overwrite cache")
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}

	meta := CacheMeta{
		URL:          t.URL,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		FetchedAt:    start.UTC(),
		Bytes:        len(body),
		Entries:      len(entries),
	}

	if err := f.cfg.Store.Write(t.URL, body, meta); err != nil {
		res.Err = fmt.Errorf("persist cache: %w", err)
		res.Duration = time.Since(start)
		f.recordOutcome(t.Name, res.Outcome)
		return res, nil
	}

	res.Outcome = FetchOutcomeSuccess
	res.Bytes = len(body)
	res.Entries = len(entries)
	res.Duration = time.Since(start)
	f.recordOutcome(t.Name, res.Outcome)
	f.markSuccess(t.Name, start)
	f.setEntries(t.Name, len(entries))
	return res, nil
}

// refreshNow buffer 1 so a SIGHUP-poke is non-blocking even mid-fetch;
// extra pokes coalesce.
const refreshChanBuf = 1

// Run drives one initial fetch cycle, then loops on the configured
// interval. Each cycle fetches every target sequentially; if at least
// one target produced a new body, onReload is invoked so the Loader
// can rebuild the matcher from disk.
//
// Run also performs a one-shot orphan sweep before the first cycle:
// cache files whose URL is not in targets (plus any leftover .tmp) are
// deleted. The sweep does not run again for the lifetime of the
// process — config changes that drop a source require restart.
//
// Returns nil when ctx is cancelled. Always honours ctx.Done().
func (f *Fetcher) Run(ctx context.Context, targets []FetchTarget, onReload func()) error {
	urls := make([]string, 0, len(targets))
	for _, t := range targets {
		urls = append(urls, t.URL)
	}
	if removed, err := f.cfg.Store.Sweep(urls); err != nil {
		f.cfg.Logger.Warn("blocklist cache sweep failed", "err", err)
	} else if removed > 0 {
		f.cfg.Logger.Info("blocklist cache swept", "removed", removed)
	}

	if onReload == nil {
		onReload = func() {}
	}

	cycle := func() {
		any := false
		for _, t := range targets {
			res, _ := f.FetchOne(ctx, t)
			switch res.Outcome {
			case FetchOutcomeSuccess:
				any = true
				f.cfg.Logger.Info("blocklist fetched",
					"source", t.Name, "outcome", string(res.Outcome),
					"bytes", res.Bytes, "entries", res.Entries,
					"duration_ms", res.Duration.Milliseconds(),
					"status", res.StatusCode)
			case FetchOutcomeNotModified:
				f.cfg.Logger.Info("blocklist fetched",
					"source", t.Name, "outcome", string(res.Outcome),
					"duration_ms", res.Duration.Milliseconds(),
					"status", res.StatusCode)
			case FetchOutcomeFailure:
				f.cfg.Logger.Warn("blocklist fetch failed",
					"source", t.Name, "err", res.Err,
					"status", res.StatusCode,
					"duration_ms", res.Duration.Milliseconds())
			}
		}
		if any {
			onReload()
		}
	}

	cycle() // initial pass

	if f.cfg.Interval <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-f.refreshNow:
				cycle()
			}
		}
	}

	ticker := time.NewTicker(f.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cycle()
		case <-f.refreshNow:
			cycle()
		}
	}
}

// RefreshNow asks Run to begin an extra fetch cycle as soon as it is
// not actively in one. Non-blocking; coalesces repeated calls (one
// pending poke is enough). Safe to call before Run starts — the poke
// is queued and consumed by Run's first select.
func (f *Fetcher) RefreshNow() {
	select {
	case f.refreshNow <- struct{}{}:
	default:
	}
}

func (f *Fetcher) recordOutcome(name string, o FetchOutcome) {
	if f.cfg.Metrics.IncFetch != nil {
		f.cfg.Metrics.IncFetch(name, o)
	}
}
func (f *Fetcher) markSuccess(name string, ts time.Time) {
	if f.cfg.Metrics.SetLastSuccess != nil {
		f.cfg.Metrics.SetLastSuccess(name, ts)
	}
}
func (f *Fetcher) setEntries(name string, n int) {
	if f.cfg.Metrics.SetEntries != nil {
		f.cfg.Metrics.SetEntries(name, n)
	}
}
