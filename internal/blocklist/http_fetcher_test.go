package blocklist_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	big := make([]byte, 65*1024*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	t.Cleanup(srv.Close)
	f := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store: blocklist.NewCacheStore(t.TempDir()), Client: srv.Client(), Interval: time.Hour,
	})
	res, err := f.FetchOne(context.Background(), blocklist.FetchTarget{Name: "x", URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, blocklist.FetchOutcomeFailure, res.Outcome)
}
