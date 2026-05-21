package blocklist_test

import (
	"context"
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestHTTPSource_LoadReturnsParsedEntriesFromCache(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)
	url := "https://example.com/pro.txt"

	body := []byte("# comment\nexample.com\n0.0.0.0 ads.example\n")
	require.NoError(t, store.Write(url, body, blocklist.CacheMeta{URL: url, Bytes: len(body), Entries: 2}))

	src := blocklist.NewHTTPSource("hagezi-pro", url, store)
	got, err := src.Load(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"example.com", "ads.example"}, got)
}

func TestHTTPSource_LoadReturnsNilOnCacheMiss(t *testing.T) {
	src := blocklist.NewHTTPSource("hagezi-pro", "https://example.com/x.txt", blocklist.NewCacheStore(t.TempDir()))
	got, err := src.Load(context.Background())
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestHTTPSource_NameAndURL(t *testing.T) {
	src := blocklist.NewHTTPSource("hagezi-pro", "https://example.com/x.txt", blocklist.NewCacheStore(t.TempDir()))
	require.Equal(t, "hagezi-pro", src.Name())
	require.Equal(t, "https://example.com/x.txt", src.URL())
}
