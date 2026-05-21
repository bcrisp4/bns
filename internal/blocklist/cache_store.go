package blocklist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrCacheMiss is returned by CacheStore.Read when no body has been
// persisted for the given URL.
var ErrCacheMiss = errors.New("blocklist cache miss")

// CacheMeta describes the on-disk state for one cached HTTP source.
// Persisted as a JSON sidecar so the body file stays a plain blocklist
// that operators can cat / wc -l.
type CacheMeta struct {
	URL          string    `json:"url"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
	Bytes        int       `json:"bytes"`
	Entries      int       `json:"entries"`
}

// CacheStore reads and writes blocklist bodies + sidecar metadata
// under a single directory. Filenames are keyed on sha256(url).
type CacheStore struct {
	dir string
}

// NewCacheStore returns a store rooted at dir. The directory is created
// on first Write; existence is not required at construction time.
func NewCacheStore(dir string) *CacheStore { return &CacheStore{dir: dir} }

// Dir returns the cache directory passed to NewCacheStore.
func (s *CacheStore) Dir() string { return s.dir }

func (s *CacheStore) bodyPath(u string) string { return filepath.Join(s.dir, hashURL(u)+".txt") }
func (s *CacheStore) metaPath(u string) string { return filepath.Join(s.dir, hashURL(u)+".meta.json") }

// Read returns the cached body and metadata for u. Returns ErrCacheMiss
// when no body is present. A missing or corrupt sidecar is non-fatal:
// Read still returns the body with a zero-valued CacheMeta.
func (s *CacheStore) Read(u string) ([]byte, CacheMeta, error) {
	body, err := os.ReadFile(s.bodyPath(u))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, CacheMeta{}, ErrCacheMiss
	}
	if err != nil {
		return nil, CacheMeta{}, fmt.Errorf("read cache body: %w", err)
	}
	var meta CacheMeta
	rawMeta, err := os.ReadFile(s.metaPath(u))
	if err == nil {
		_ = json.Unmarshal(rawMeta, &meta) // best-effort
	}
	return body, meta, nil
}

// Write atomically persists body + meta for u. cache_dir is created if
// missing. Body and meta are written to .tmp files, fsynced, then
// renamed into place so readers never observe a partial write.
func (s *CacheStore) Write(u string, body []byte, meta CacheMeta) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("mkdir cache_dir: %w", err)
	}
	if err := writeFileAtomic(s.bodyPath(u), body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := writeFileAtomic(s.metaPath(u), metaJSON); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	return nil
}

// Sweep removes every file in the cache directory that does not belong
// to one of the URLs in keep. Also removes leftover .tmp files. Returns
// the number of files removed.
//
// Intended for one-shot startup cleanup. Not safe to run concurrently
// with Write to the same store; the caller must serialise.
func (s *CacheStore) Sweep(keep []string) (int, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cache_dir: %w", err)
	}
	keepSet := make(map[string]struct{}, len(keep)*2)
	for _, u := range keep {
		h := hashURL(u)
		keepSet[h+".txt"] = struct{}{}
		keepSet[h+".meta.json"] = struct{}{}
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if _, ok := keepSet[name]; ok {
			continue
		}
		if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".meta.json") {
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				return removed, fmt.Errorf("remove orphan %s: %w", name, err)
			}
			removed++
		}
	}
	return removed, nil
}

func hashURL(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:])
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
