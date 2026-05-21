package blocklist_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestCacheStore_WriteThenReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)

	body := []byte("example.com\nads.example\n")
	meta := blocklist.CacheMeta{
		URL:          "https://example.com/list.txt",
		ETag:         `"abc123"`,
		LastModified: "Wed, 21 Oct 2020 07:28:00 GMT",
		FetchedAt:    time.Unix(1700000000, 0).UTC(),
		Bytes:        len(body),
		Entries:      2,
	}
	require.NoError(t, store.Write(meta.URL, body, meta))

	gotBody, gotMeta, err := store.Read(meta.URL)
	require.NoError(t, err)
	require.Equal(t, body, gotBody)
	require.Equal(t, meta.ETag, gotMeta.ETag)
	require.Equal(t, meta.LastModified, gotMeta.LastModified)
	require.Equal(t, meta.Bytes, gotMeta.Bytes)
	require.Equal(t, meta.Entries, gotMeta.Entries)
}

func TestCacheStore_ReadMissingReturnsNotFound(t *testing.T) {
	store := blocklist.NewCacheStore(t.TempDir())
	_, _, err := store.Read("https://nope.example/x")
	require.ErrorIs(t, err, blocklist.ErrCacheMiss)
}

func TestCacheStore_AtomicRename_PartialTmpDoesNotCorruptReader(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)
	url := "https://example.com/list.txt"

	require.NoError(t, store.Write(url, []byte("first\n"), blocklist.CacheMeta{URL: url, Bytes: 6, Entries: 1}))

	tmp := filepath.Join(dir, sha256hex(url)+".txt.tmp")
	require.NoError(t, os.WriteFile(tmp, []byte("partial-garbage"), 0o644))

	body, _, err := store.Read(url)
	require.NoError(t, err)
	require.Equal(t, []byte("first\n"), body)
}

func TestCacheStore_SweepRemovesOrphans(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)

	keepURL := "https://example.com/keep.txt"
	dropURL := "https://example.com/drop.txt"

	require.NoError(t, store.Write(keepURL, []byte("k\n"), blocklist.CacheMeta{URL: keepURL, Bytes: 2, Entries: 1}))
	require.NoError(t, store.Write(dropURL, []byte("d\n"), blocklist.CacheMeta{URL: dropURL, Bytes: 2, Entries: 1}))

	// A leftover .tmp from an interrupted write is sha256-named.
	interruptedTmp := filepath.Join(dir, sha256hex("https://example.com/interrupted.txt")+".txt.tmp")
	require.NoError(t, os.WriteFile(interruptedTmp, []byte("partial"), 0o644))

	// A non-cache file the operator left in the directory must NOT be deleted.
	operatorFile := filepath.Join(dir, "operator-notes.txt")
	require.NoError(t, os.WriteFile(operatorFile, []byte("keep me"), 0o644))

	removed, err := store.Sweep([]string{keepURL})
	require.NoError(t, err)
	// Expect exactly 3 removals: dropURL body + dropURL meta + interrupted tmp.
	require.Equal(t, 3, removed)

	_, _, err = store.Read(keepURL)
	require.NoError(t, err)
	_, _, err = store.Read(dropURL)
	require.ErrorIs(t, err, blocklist.ErrCacheMiss)

	// Drop's meta sidecar gone.
	dropMeta := filepath.Join(dir, sha256hex(dropURL)+".meta.json")
	_, err = os.Stat(dropMeta)
	require.True(t, os.IsNotExist(err))

	// Interrupted .tmp gone.
	_, err = os.Stat(interruptedTmp)
	require.True(t, os.IsNotExist(err))

	// Operator file preserved.
	_, err = os.Stat(operatorFile)
	require.NoError(t, err)
}

func TestCacheStore_MetaJSONIsHumanReadable(t *testing.T) {
	dir := t.TempDir()
	store := blocklist.NewCacheStore(dir)
	url := "https://example.com/list.txt"
	require.NoError(t, store.Write(url, []byte("x\n"), blocklist.CacheMeta{URL: url, Bytes: 2, Entries: 1, ETag: `"e"`}))

	metaPath := filepath.Join(dir, sha256hex(url)+".meta.json")
	raw, err := os.ReadFile(metaPath)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, url, decoded["url"])
}
