package blocklist_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestFetcher_FetchOne_200WritesCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2020 07:28:00 GMT")
		_, _ = w.Write([]byte("example.com\nads.example\n"))
	}))
	t.Cleanup(srv.Close)

	store := blocklist.NewCacheStore(t.TempDir())
	f := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store:    store,
		Client:   srv.Client(),
		Interval: time.Hour,
	})

	res, err := f.FetchOne(context.Background(), blocklist.FetchTarget{Name: "x", URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeSuccess, res.Outcome)
	require.Equal(t, 2, res.Entries)
	require.Greater(t, res.Bytes, 0)

	body, meta, err := store.Read(srv.URL)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.Equal(t, `"v1"`, meta.ETag)
	require.Equal(t, 2, meta.Entries)
}

func TestFetcher_FetchOne_304LeavesCacheUntouched(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("example.com\n"))
	}))
	t.Cleanup(srv.Close)

	store := blocklist.NewCacheStore(t.TempDir())
	f := blocklist.NewFetcher(blocklist.FetcherConfig{Store: store, Client: srv.Client(), Interval: time.Hour})
	target := blocklist.FetchTarget{Name: "x", URL: srv.URL}

	res1, err := f.FetchOne(context.Background(), target)
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeSuccess, res1.Outcome)

	res2, err := f.FetchOne(context.Background(), target)
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeNotModified, res2.Outcome)
	require.Equal(t, 0, res2.Bytes)
	require.Equal(t, 2, calls)
}

func TestFetcher_FetchOne_5xxKeepsCacheReturnsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	store := blocklist.NewCacheStore(t.TempDir())
	url := srv.URL
	require.NoError(t, store.Write(url, []byte("example.com\n"), blocklist.CacheMeta{URL: url, Bytes: 12, Entries: 1}))

	f := blocklist.NewFetcher(blocklist.FetcherConfig{Store: store, Client: srv.Client(), Interval: time.Hour})
	res, err := f.FetchOne(context.Background(), blocklist.FetchTarget{Name: "x", URL: url})
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeFailure, res.Outcome)

	body, _, err := store.Read(url)
	require.NoError(t, err)
	require.Equal(t, []byte("example.com\n"), body)
}

func TestFetcher_FetchOne_RejectsBodyOverMaxSize(t *testing.T) {
	// Stream 65 MiB from the handler so the test process doesn't hold
	// the buffer in memory. Just-over-cap (64 MiB + 1) is enough to
	// trip the guard.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64*1024) // 64 KiB scratch
		const total = 65 * 1024 * 1024
		written := 0
		for written < total {
			n := total - written
			if n > len(chunk) {
				n = len(chunk)
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			written += n
		}
	}))
	t.Cleanup(srv.Close)
	f := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store: blocklist.NewCacheStore(t.TempDir()), Client: srv.Client(), Interval: time.Hour,
	})
	res, err := f.FetchOne(context.Background(), blocklist.FetchTarget{Name: "x", URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeFailure, res.Outcome)
}

func TestFetcher_Run_TriggersFetchOnRefreshNow(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("example.com\n"))
	}))
	t.Cleanup(srv.Close)

	store := blocklist.NewCacheStore(t.TempDir())
	var reloaded int32
	f := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store: store, Client: srv.Client(),
		Interval: time.Hour, // long; we drive via refreshNow
	})
	targets := []blocklist.FetchTarget{{Name: "x", URL: srv.URL}}
	onReload := func() { atomic.AddInt32(&reloaded, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { _ = f.Run(ctx, targets, onReload); close(done) }()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reloaded) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	f.RefreshNow()
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&hits) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestFetcher_Run_SweepsOrphansOnStartup(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)
	require.NoError(t, store.Write("https://orphan.example/x", []byte("a\n"), blocklist.CacheMeta{URL: "https://orphan.example/x", Bytes: 2, Entries: 1}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("example.com\n"))
	}))
	t.Cleanup(srv.Close)

	f := blocklist.NewFetcher(blocklist.FetcherConfig{Store: store, Client: srv.Client(), Interval: time.Hour})
	targets := []blocklist.FetchTarget{{Name: "x", URL: srv.URL}}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { _ = f.Run(ctx, targets, func() {}); close(done) }()

	require.Eventually(t, func() bool {
		_, _, err := store.Read("https://orphan.example/x")
		return errors.Is(err, blocklist.ErrCacheMiss)
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}

func TestFetcher_Run_CallsReloadOnlyWhenSomethingChanged(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("example.com\n"))
	}))
	t.Cleanup(srv.Close)

	store := blocklist.NewCacheStore(t.TempDir())
	var reloaded int32
	f := blocklist.NewFetcher(blocklist.FetcherConfig{Store: store, Client: srv.Client(), Interval: time.Hour})
	targets := []blocklist.FetchTarget{{Name: "x", URL: srv.URL}}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { _ = f.Run(ctx, targets, func() { atomic.AddInt32(&reloaded, 1) }); close(done) }()

	// First fetch is a 200 → reload fires once.
	require.Eventually(t, func() bool { return atomic.LoadInt32(&reloaded) == 1 }, 2*time.Second, 10*time.Millisecond)

	// Trigger a second cycle; server returns 304.
	f.RefreshNow()
	require.Eventually(t, func() bool { return atomic.LoadInt32(&hits) >= 2 }, 2*time.Second, 10*time.Millisecond)

	// Reload count must still be 1 (304 = no change).
	require.Equal(t, int32(1), atomic.LoadInt32(&reloaded))

	cancel()
	<-done
}
