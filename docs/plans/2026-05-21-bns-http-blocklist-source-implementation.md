# BNS HTTP Blocklist Source Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an HTTP blocklist source with polite auto-refresh, on-disk persistence, per-source observability, and a bootstrap dialer that lets BNS fetch its own lists even when it is the sole resolver on the network. Matches the design in `docs/specs/2026-05-21-bns-http-blocklist-source-design.md`.

**Architecture:** A new `Fetcher` goroutine runs alongside the existing listeners and SIGHUP handler under the same `errgroup`. Network I/O is owned by the `Fetcher`; `HTTPSource.Load()` is disk-only and synchronous, so the request-serving path never blocks on a network fetch. The fetcher writes the raw body + sidecar metadata atomically under `cache_dir` and, on success, triggers a single `loader.Reload()` path that rebuilds the matcher from every source. A custom `net.Resolver` routes DNS lookups for upstream URL hosts via the configured upstream IP set (not through the BNS resolver chain — that would deadlock).

**Tech Stack:** Go 1.26; stdlib `net/http`, `net.Resolver`; `prometheus/client_golang`; stdlib `log/slog`; `codeberg.org/miekg/dns` only via the existing upstream pool (bootstrap dialer dials raw TCP/UDP and lets the stdlib pure-Go resolver handle DNS framing).

**Order rationale:** Build inside-out so each layer is testable before the one above. Foundation (config schema) → leaf packages with no deps (cache_store, bootstrap_dialer) → composed types (http_source, http_fetcher) → integration glue (loader, serve.go) → end-to-end test → deploy/docs.

**Commit cadence:** Each task ends in a commit. Subject ≤ 50 chars. Reference the spec section (e.g. `(spec §5.5)`) when useful. Trailer:

```
Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

## Required and recommended skills

Same baseline as `2026-05-19-bns-mvp-implementation.md` (process discipline + repo Go skills). Specific to this plan:

| Skill | Why |
| ----- | --- |
| `superpowers:test-driven-development` | Every task is RED-GREEN-REFACTOR. |
| `superpowers:verification-before-completion` | Run `make race` after each task. Never claim "passes" without output. |
| `cc-skills-golang:golang-concurrency` | Fetcher goroutine, ticker, `refreshNow` channel, context cancellation, goleak — load-bearing. |
| `cc-skills-golang:golang-testing` | `httptest.NewServer` for fake upstream; `t.TempDir` for cache_dir; injectable clock for ticker; goleak per package. |
| `cc-skills-golang:golang-context` | HTTP request ctx, ticker select loop, propagation into custom resolver. |
| `cc-skills-golang:golang-observability` | `bns_blocklist_fetch_total{source,outcome}` + per-source gauges; structured slog with consistent field names. |
| `cc-skills-golang:golang-error-handling` | Wrap with `%w` at every fetcher boundary; never log-and-return. |
| `cc-skills-golang:golang-security` | TLS standard validation, body size limits, URL scheme allowlist. |
| `cc-skills-golang:golang-spf13-viper` | New `blocklists.refresh_interval` and `blocklists.cache_dir` env binds. |

---

## File map

**New files (production):**

- `internal/blocklist/cache_store.go` — atomic read/write for body + `.meta.json`; startup orphan sweep helper.
- `internal/blocklist/http_source.go` — `HTTPSource` implements `Source`; reads cached body from disk only.
- `internal/blocklist/http_fetcher.go` — `Fetcher` goroutine; HTTP client; ticker loop; one fetch cycle.
- `internal/blocklist/bootstrap_dialer.go` — custom `*net.Resolver` that dials configured upstream IPs.

**New files (tests):**

- `internal/blocklist/cache_store_test.go`
- `internal/blocklist/http_source_test.go`
- `internal/blocklist/http_fetcher_test.go`
- `internal/blocklist/bootstrap_dialer_test.go`
- `internal/integration/http_blocklist_test.go`

**Modified production files:**

- `internal/config/config.go` — add `Blocklists.RefreshInterval`, `Blocklists.CacheDir`; add `BlocklistSource.Name`, `BlocklistSource.URL`.
- `internal/config/defaults.go` — `RefreshInterval = 24h`, `CacheDir = "/var/cache/bns/blocklists"`.
- `internal/config/validate.go` — `name` required + unique; `type` ∈ {file, http}; `url` parses with scheme http/https; `refresh_interval ≥ 1m`.
- `internal/blocklist/loader.go` — unchanged interface; only the construction path in `serve.go` changes (loader gains http sources transparently).
- `internal/metrics/metrics.go` — add `BlocklistFetchTotal{source,outcome}`, `BlocklistLastSuccessTimestamp{source}`, `BlocklistEntriesBySource{source}`.
- `cmd/bns/serve.go` — drop `--blocklist` flag, build sources from `config.Blocklists.Sources`, wire `Fetcher` into errgroup, fold SIGHUP into refreshNow + Reload.

**Modified tests:**

- `internal/config/config_test.go`, `validate_test.go` — cover new fields.
- `internal/blocklist/loader_test.go` — cover HTTP source path.

**Deploy / docs:**

- `deploy/docker/Dockerfile` — drop `hagezi-fetch` stage, drop `HAGEZI_TAG` ARG, add `/var/cache/bns` ownership.
- `deploy/docker/config.yaml` — switch to hagezi http source.
- `examples/config.example.yaml` — rewrite blocklists block.
- `README.md` — document new keys, removed flag, metrics, volume mount.
- `CLAUDE.md` — same.
- `docs/TODO.md` — append §10 deferred items.

---

## Task 1: Config schema — new fields

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go` (read the file first to find an appropriate insertion point; if no `config_test.go` test exists for the schema yet, create the function fresh):

```go
func TestBlocklistsSchema_AcceptsHTTPSourceAndGlobalKeys(t *testing.T) {
	yaml := []byte(`
listen: {udp: ":53", tcp: ":53", query_timeout: 5s}
upstreams:
  - {addr: "1.1.1.1:53", timeout: 2s}
cache: {capacity: 1000, min_ttl: 0s, max_ttl: 1h, negative_ttl_max: 5m}
blocklists:
  refresh_interval: 12h
  cache_dir: /tmp/bns-cache
  sources:
    - {type: file, name: custom, path: /etc/custom.txt}
    - {type: http, name: hagezi-pro, url: "https://example.com/pro.txt"}
admin: {listen: ":9090"}
logging: {level: info, format: json}
shutdown_timeout: 5s
startup_probe_timeout: 3s
`)
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, yaml, 0o644))

	cfg, err := config.Load(config.LoadOptions{ConfigPath: path})
	require.NoError(t, err)
	require.Equal(t, 12*time.Hour, cfg.Blocklists.RefreshInterval)
	require.Equal(t, "/tmp/bns-cache", cfg.Blocklists.CacheDir)
	require.Len(t, cfg.Blocklists.Sources, 2)
	require.Equal(t, "file", cfg.Blocklists.Sources[0].Type)
	require.Equal(t, "custom", cfg.Blocklists.Sources[0].Name)
	require.Equal(t, "/etc/custom.txt", cfg.Blocklists.Sources[0].Path)
	require.Equal(t, "http", cfg.Blocklists.Sources[1].Type)
	require.Equal(t, "hagezi-pro", cfg.Blocklists.Sources[1].Name)
	require.Equal(t, "https://example.com/pro.txt", cfg.Blocklists.Sources[1].URL)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/config/ -run TestBlocklistsSchema_AcceptsHTTPSourceAndGlobalKeys -v
```

Expected: FAIL with compile error (`RefreshInterval`, `CacheDir`, `Name`, `URL` undefined on the structs).

- [ ] **Step 3: Add fields to the structs**

In `internal/config/config.go`, replace the `Blocklists` and `BlocklistSource` types:

```go
// Blocklists configures the ad-blocking blocklist sources and the
// background HTTP fetcher.
type Blocklists struct {
	RefreshInterval time.Duration     `mapstructure:"refresh_interval"`
	CacheDir        string            `mapstructure:"cache_dir"`
	Sources         []BlocklistSource `mapstructure:"sources"`
}

// BlocklistSource is one source of blocklist entries.
//
// Name is required and used as the metric label and log field. It must
// be unique across sources.
type BlocklistSource struct {
	Type string `mapstructure:"type"` // "file" | "http"
	Name string `mapstructure:"name"`
	Path string `mapstructure:"path"` // for type="file"
	URL  string `mapstructure:"url"`  // for type="http"
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/config/ -run TestBlocklistsSchema_AcceptsHTTPSourceAndGlobalKeys -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: add http blocklist source schema (spec §4)"
```

---

## Task 2: Config defaults

**Files:**
- Modify: `internal/config/defaults.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestDefault_BlocklistsKeysPresent(t *testing.T) {
	d := config.Default()
	require.Equal(t, 24*time.Hour, d.Blocklists.RefreshInterval)
	require.Equal(t, "/var/cache/bns/blocklists", d.Blocklists.CacheDir)
	require.Nil(t, d.Blocklists.Sources)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/config/ -run TestDefault_BlocklistsKeysPresent -v
```

Expected: FAIL (`RefreshInterval` and `CacheDir` are zero values).

- [ ] **Step 3: Update defaults**

In `internal/config/defaults.go`, replace the `Blocklists` line:

```go
Blocklists: Blocklists{
    RefreshInterval: 24 * time.Hour,
    CacheDir:        "/var/cache/bns/blocklists",
},
```

- [ ] **Step 4: Run all config tests**

```
go test ./internal/config/ -v
```

Expected: PASS for new and existing tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/defaults.go internal/config/config_test.go
git commit -m "config: defaults for blocklists refresh + cache_dir"
```

---

## Task 3: Config validation

**Files:**
- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/validate_test.go`:

```go
func TestValidate_BlocklistSource_NameRequired(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = config.Listen{UDP: ":53", TCP: ":53", QueryTimeout: time.Second}
	cfg.Admin = config.Admin{Listen: ":9090"}
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: time.Second}}
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "file", Path: "/x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "name")
}

func TestValidate_BlocklistSource_NameUnique(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{
		{Type: "file", Name: "dup", Path: "/a"},
		{Type: "file", Name: "dup", Path: "/b"},
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "unique")
}

func TestValidate_BlocklistSource_HTTPRequiresURL(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "http", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "url")
}

func TestValidate_BlocklistSource_HTTPURLSchemeAllowed(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "http", Name: "x", URL: "ftp://example.com/x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")
}

func TestValidate_BlocklistSource_FileRequiresPath(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "file", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "path")
}

func TestValidate_BlocklistSource_TypeAllowlist(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.Sources = []config.BlocklistSource{{Type: "ftp", Name: "x"}}
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "type")
}

func TestValidate_RefreshInterval_MinFloor(t *testing.T) {
	cfg := validatableConfig()
	cfg.Blocklists.RefreshInterval = 30 * time.Second
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "refresh_interval")
}

// validatableConfig returns a config that passes Validate() apart from
// any tweaks the caller applies.
func validatableConfig() config.Config {
	cfg := config.Default()
	cfg.Listen = config.Listen{UDP: ":53", TCP: ":53", QueryTimeout: time.Second}
	cfg.Admin = config.Admin{Listen: ":9090"}
	cfg.Upstreams = []config.Upstream{{Addr: "1.1.1.1:53", Timeout: time.Second}}
	return cfg
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/config/ -run TestValidate_Blocklist -v
go test ./internal/config/ -run TestValidate_RefreshInterval -v
```

Expected: FAIL (existing `Validate` does not check these).

- [ ] **Step 3: Extend `Validate`**

In `internal/config/validate.go`, append before the final `return nil`:

```go
if c.Blocklists.RefreshInterval > 0 && c.Blocklists.RefreshInterval < time.Minute {
    return fmt.Errorf("blocklists.refresh_interval %s must be >= 1m (or 0 to disable refresh)", c.Blocklists.RefreshInterval)
}
seenNames := make(map[string]struct{}, len(c.Blocklists.Sources))
for i, s := range c.Blocklists.Sources {
    if s.Name == "" {
        return fmt.Errorf("blocklists.sources[%d].name is required", i)
    }
    if _, dup := seenNames[s.Name]; dup {
        return fmt.Errorf("blocklists.sources[%d].name %q must be unique", i, s.Name)
    }
    seenNames[s.Name] = struct{}{}
    switch s.Type {
    case "file":
        if s.Path == "" {
            return fmt.Errorf("blocklists.sources[%d] (%s): path is required for type=file", i, s.Name)
        }
    case "http":
        if s.URL == "" {
            return fmt.Errorf("blocklists.sources[%d] (%s): url is required for type=http", i, s.Name)
        }
        u, err := url.Parse(s.URL)
        if err != nil {
            return fmt.Errorf("blocklists.sources[%d] (%s): url %q: %w", i, s.Name, s.URL, err)
        }
        if u.Scheme != "http" && u.Scheme != "https" {
            return fmt.Errorf("blocklists.sources[%d] (%s): url scheme %q must be http or https", i, s.Name, u.Scheme)
        }
    default:
        return fmt.Errorf("blocklists.sources[%d] (%s): type %q must be file or http", i, s.Name, s.Type)
    }
}
```

Add `"net/url"` and `"time"` to the imports if missing.

The `RefreshInterval > 0` guard allows operators to explicitly disable refresh by setting `0`; the floor only kicks in for positive values.

- [ ] **Step 4: Run all config tests**

```
go test ./internal/config/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "config: validate blocklist source fields (spec §4)"
```

---

## Task 4: cache_store — read/write atomic with sidecar

**Files:**
- Create: `internal/blocklist/cache_store.go`
- Create: `internal/blocklist/cache_store_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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

	// Plant a previous successful body.
	require.NoError(t, store.Write(url, []byte("first\n"), blocklist.CacheMeta{URL: url, Bytes: 6, Entries: 1}))

	// Simulate crash mid-write: leave a .tmp behind, do not rename.
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
	// Also leave a stray .tmp.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stray.txt.tmp"), []byte("x"), 0o644))

	removed, err := store.Sweep([]string{keepURL})
	require.NoError(t, err)
	require.GreaterOrEqual(t, removed, 1)

	// keepURL still readable.
	_, _, err = store.Read(keepURL)
	require.NoError(t, err)
	// dropURL gone.
	_, _, err = store.Read(dropURL)
	require.ErrorIs(t, err, blocklist.ErrCacheMiss)
	// stray gone.
	_, err = os.Stat(filepath.Join(dir, "stray.txt.tmp"))
	require.True(t, os.IsNotExist(err))
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
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/blocklist/ -run TestCacheStore -v
```

Expected: FAIL with compile error (`NewCacheStore`, `CacheMeta`, `ErrCacheMiss` undefined).

- [ ] **Step 3: Implement cache_store**

```go
// internal/blocklist/cache_store.go
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
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/blocklist/ -run TestCacheStore -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/blocklist/cache_store.go internal/blocklist/cache_store_test.go
git commit -m "blocklist: cache_store with atomic write + sweep (spec §5.6)"
```

---

## Task 5: HTTPSource — disk-only Source implementation

**Files:**
- Create: `internal/blocklist/http_source.go`
- Create: `internal/blocklist/http_source_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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

func TestHTTPSource_LoadReturnsEmptyOnCacheMiss(t *testing.T) {
	src := blocklist.NewHTTPSource("hagezi-pro", "https://example.com/x.txt", blocklist.NewCacheStore(t.TempDir()))
	got, err := src.Load(context.Background())
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestHTTPSource_Name(t *testing.T) {
	src := blocklist.NewHTTPSource("hagezi-pro", "https://example.com/x.txt", blocklist.NewCacheStore(t.TempDir()))
	require.Equal(t, "hagezi-pro", src.Name())
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/blocklist/ -run TestHTTPSource -v
```

Expected: FAIL (`NewHTTPSource` undefined).

- [ ] **Step 3: Implement HTTPSource**

```go
// internal/blocklist/http_source.go
package blocklist

import (
	"context"
	"errors"
	"fmt"
)

// HTTPSource is a Source whose entries come from a remote URL but whose
// Load reads only from the on-disk cache. The cache is populated by a
// separate Fetcher goroutine; HTTPSource never makes network calls,
// keeping Load synchronous and the request-serving path off the network.
type HTTPSource struct {
	name  string
	url   string
	store *CacheStore
}

// NewHTTPSource constructs an HTTPSource. name is used as the metric
// label and log field; url is the remote location and the cache key.
func NewHTTPSource(name, url string, store *CacheStore) *HTTPSource {
	return &HTTPSource{name: name, url: url, store: store}
}

// Name returns the operator-supplied source name.
func (s *HTTPSource) Name() string { return s.name }

// URL returns the configured remote URL.
func (s *HTTPSource) URL() string { return s.url }

// Load parses the cached body and returns canonicalised FQDNs. Returns
// an empty slice (no error) when no cache file exists; the Fetcher will
// populate it asynchronously and a later Reload will pick it up.
func (s *HTTPSource) Load(_ context.Context) ([]string, error) {
	body, _, err := s.store.Read(s.url)
	if errors.Is(err, ErrCacheMiss) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("http source %q: %w", s.name, err)
	}
	return parseBody(body), nil
}

// parseBody runs the byte buffer through ParseLine and returns the
// surviving canonicalised entries. Reuses the file_source parsing rules.
func parseBody(body []byte) []string {
	out := make([]string, 0, 1024)
	start := 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == '\n' {
			line := string(body[start:i])
			if fqdn, ok := ParseLine(line); ok {
				out = append(out, fqdn)
			}
			start = i + 1
		}
	}
	return out
}
```

If `ParseLine` is not exported, export it now (alias if necessary). Verify with:

```
grep -n 'func ParseLine' internal/blocklist/parse.go
```

If only unexported `parseLine` exists, export it as `ParseLine` and update the existing caller in `file_source.go`.

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/blocklist/ -run TestHTTPSource -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/blocklist/http_source.go internal/blocklist/http_source_test.go internal/blocklist/parse.go internal/blocklist/file_source.go
git commit -m "blocklist: HTTPSource backed by on-disk cache (spec §5.2)"
```

(Only stage `parse.go` / `file_source.go` if you needed to export `ParseLine`.)

---

## Task 6: Bootstrap dialer — net.Resolver via upstream IPs

**Files:**
- Create: `internal/blocklist/bootstrap_dialer.go`
- Create: `internal/blocklist/bootstrap_dialer_test.go`

- [ ] **Step 1: Write the failing test**

The test boots an in-process miekg/dns server (using existing `internal/upstream/testutil`) that answers A queries for one synthetic name, then verifies the bootstrap resolver returns that answer instead of consulting the system stub.

```go
package blocklist_test

import (
	"context"
	"net"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
	"codeberg.org/miekg/dns/rdata"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/stretchr/testify/require"
)

func TestBootstrapResolver_ResolvesViaConfiguredUpstream(t *testing.T) {
	handler := dns.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) {
		resp := new(dns.Msg)
		dnsutil.SetReply(resp, r)
		resp.Answer = []dns.RR{&dns.A{
			Hdr: dns.Header{Name: "fake.test.", Class: dns.ClassINET, TTL: 60},
			A:   rdata.A{Addr: net.ParseIP("127.0.0.99").To4().IP4()},
		}}
		_, _ = resp.WriteTo(w)
	})
	addr := testutil.SpawnServer(t, handler) // existing helper returns "127.0.0.1:PORT"

	res := blocklist.NewBootstrapResolver([]string{addr})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := res.LookupHost(ctx, "fake.test")
	require.NoError(t, err)
	require.Contains(t, ips, "127.0.0.99")
}
```

Note: the exact `rdata.A` shape may differ (see `CLAUDE.md` cheat sheet — `A: rdata.A{Addr: netip.MustParseAddr("1.2.3.4")}` is the documented form; use that variant if `net.ParseIP` plus `.To4().IP4()` doesn't compile under v2).

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/blocklist/ -run TestBootstrapResolver -v
```

Expected: FAIL (`NewBootstrapResolver` undefined).

- [ ] **Step 3: Implement bootstrap dialer**

```go
// internal/blocklist/bootstrap_dialer.go
package blocklist

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
)

// NewBootstrapResolver returns a *net.Resolver that resolves DNS names
// by dialing one of upstreamAddrs directly, bypassing the system stub.
// The stdlib pure-Go resolver handles DNS framing and TCP fallback; the
// dialer only supplies a connection to an upstream of our choosing.
//
// upstreamAddrs are "host:port" forms identical to those in the BNS
// upstream pool. They are used for their IP set only — this resolver
// MUST NOT be wired through the BNS resolver chain (deadlock).
func NewBootstrapResolver(upstreamAddrs []string) *net.Resolver {
	if len(upstreamAddrs) == 0 {
		return net.DefaultResolver
	}
	addrs := append([]string(nil), upstreamAddrs...)
	var rr uint32
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		if len(addrs) == 0 {
			return nil, errors.New("bootstrap resolver: no upstreams configured")
		}
		var lastErr error
		// Round-robin over upstreams; first reachable wins. Mirrors Pool's
		// "try each in order" behaviour but is dial-only — DNS framing is
		// the stdlib resolver's job.
		start := int(atomic.AddUint32(&rr, 1)) % len(addrs)
		for i := 0; i < len(addrs); i++ {
			addr := addrs[(start+i)%len(addrs)]
			d := net.Dialer{}
			c, err := d.DialContext(ctx, network, addr)
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &net.Resolver{
		PreferGo: true,
		Dial:     dial,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```
go test ./internal/blocklist/ -run TestBootstrapResolver -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/blocklist/bootstrap_dialer.go internal/blocklist/bootstrap_dialer_test.go
git commit -m "blocklist: bootstrap resolver via upstream IPs (spec §5.5)"
```

---

## Task 7: HTTP fetcher — single fetch unit (`fetchOne`)

This task implements only the per-source fetch primitive. The ticker and run loop come in Task 8. Splitting keeps tests focused and the diff reviewable.

**Files:**
- Create: `internal/blocklist/http_fetcher.go`
- Create: `internal/blocklist/http_fetcher_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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
		Interval: time.Hour, // unused in this test
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
	// Seed cache with a previous good body.
	require.NoError(t, store.Write(url, []byte("example.com\n"), blocklist.CacheMeta{URL: url, Bytes: 12, Entries: 1}))

	f := blocklist.NewFetcher(blocklist.FetcherConfig{Store: store, Client: srv.Client(), Interval: time.Hour})
	res, err := f.FetchOne(context.Background(), blocklist.FetchTarget{Name: "x", URL: url})
	require.NoError(t, err) // fetchOne returns the outcome via res, not Go err
	require.Equal(t, blocklist.FetchOutcomeFailure, res.Outcome)

	body, _, err := store.Read(url)
	require.NoError(t, err)
	require.Equal(t, []byte("example.com\n"), body)
}

func TestFetcher_FetchOne_RejectsBodyOverMaxSize(t *testing.T) {
	big := make([]byte, 65*1024*1024) // 65 MiB > 64 MiB cap
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
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/blocklist/ -run TestFetcher_FetchOne -v
```

Expected: FAIL (`Fetcher`, `FetcherConfig`, `FetchTarget`, `FetchOutcome*` undefined).

- [ ] **Step 3: Implement Fetcher (fetch primitive only)**

```go
// internal/blocklist/http_fetcher.go
package blocklist

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
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
	Store    *CacheStore
	Client   *http.Client
	Interval time.Duration  // global refresh interval; <=0 disables ticker
	Logger   *slog.Logger   // optional; nil → discard
	Metrics  FetcherMetrics // optional; zero value → no-op
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
// fresh. FetchOne is the smallest unit; Run drives the ticker and is
// added in Task 8.
type Fetcher struct {
	cfg  FetcherConfig
	mu   sync.Mutex // serialises Write/Sweep against the store
}

// NewFetcher constructs a Fetcher. Client is the only required
// dependency; tests typically pass httptest.NewServer.Client().
func NewFetcher(cfg FetcherConfig) *Fetcher {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: defaultFetchTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "bns/dev (+https://github.com/bcrisp4/bns)"
	}
	return &Fetcher{cfg: cfg}
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
		// Probably an HTML error page or empty cooked response. Refuse to
		// replace cache with garbage.
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

	f.mu.Lock()
	err = f.cfg.Store.Write(t.URL, body, meta)
	f.mu.Unlock()
	if err != nil {
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
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/blocklist/ -run TestFetcher_FetchOne -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/blocklist/http_fetcher.go internal/blocklist/http_fetcher_test.go
git commit -m "blocklist: Fetcher.FetchOne with conditional GET (spec §5.4)"
```

---

## Task 8: Fetcher.Run — ticker, refreshNow, orphan sweep

**Files:**
- Modify: `internal/blocklist/http_fetcher.go`
- Modify: `internal/blocklist/http_fetcher_test.go`

- [ ] **Step 1: Write the failing tests**

```go
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

	// Initial pass fires immediately; wait for first reload.
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&reloaded) >= 1
	}, 2*time.Second, 10*time.Millisecond)

	// Trigger an extra refresh — server returns 304 since ETag matches.
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
	// Plant an orphan body that is not in the targets.
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// Wait for the initial pass to reload exactly once.
	require.Eventually(t, func() bool { return atomic.LoadInt32(&reloaded) == 1 }, 2*time.Second, 10*time.Millisecond)

	// Trigger again; server returns 304 → no reload.
	f.RefreshNow()
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, int32(1), atomic.LoadInt32(&reloaded))

	cancel()
	<-done
}
```

Add `"sync/atomic"` and `"errors"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./internal/blocklist/ -run TestFetcher_Run -v
```

Expected: FAIL (`Run`, `RefreshNow` undefined).

- [ ] **Step 3: Extend Fetcher with Run + RefreshNow**

Append to `internal/blocklist/http_fetcher.go`:

```go
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
	// One-shot orphan sweep (spec §5.3).
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

	f.refreshNow = make(chan struct{}, refreshChanBuf)

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
		// Refresh disabled; only respond to RefreshNow + ctx.
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
// pending poke is enough).
func (f *Fetcher) RefreshNow() {
	if f.refreshNow == nil {
		return
	}
	select {
	case f.refreshNow <- struct{}{}:
	default:
	}
}
```

Add `refreshNow chan struct{}` to the `Fetcher` struct definition.

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/blocklist/ -run TestFetcher -v -race
```

Expected: PASS for all `TestFetcher_*` cases.

- [ ] **Step 5: Add goleak guard for the blocklist package**

If `internal/blocklist/main_test.go` does not exist, create it:

```go
package blocklist_test

import (
	"testing"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
```

Run `go get go.uber.org/goleak` if not already in go.mod.

Re-run:

```
go test ./internal/blocklist/ -v -race
```

Expected: PASS; no leaked goroutines.

- [ ] **Step 6: Commit**

```bash
git add internal/blocklist/http_fetcher.go internal/blocklist/http_fetcher_test.go internal/blocklist/main_test.go go.mod go.sum
git commit -m "blocklist: Fetcher.Run with ticker + sweep (spec §5.3)"
```

---

## Task 9: Loader integration — construct sources from config

**Files:**
- Modify: `cmd/bns/serve.go`
- Modify: existing `internal/blocklist/loader_test.go` (add a test that mixes file + http sources via the in-tree types)

- [ ] **Step 1: Write the failing test**

Append to `internal/blocklist/loader_test.go`:

```go
func TestLoader_MixesFileAndHTTPSources(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "list.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("a.example\n"), 0o644))

	cacheDir := filepath.Join(dir, "cache")
	store := blocklist.NewCacheStore(cacheDir)
	url := "https://example.com/x.txt"
	require.NoError(t, store.Write(url, []byte("b.example\n"), blocklist.CacheMeta{URL: url, Bytes: 10, Entries: 1}))

	l := blocklist.NewLoader([]blocklist.Source{
		blocklist.FileSource{Path: filePath},
		blocklist.NewHTTPSource("x", url, store),
	})
	m, _, err := l.Load(context.Background())
	require.NoError(t, err)
	require.True(t, m.Match("a.example"))
	require.True(t, m.Match("b.example"))
}
```

- [ ] **Step 2: Run test to verify it passes**

`Loader` itself does not need code changes — the interface already accepts any `Source`. The test just exercises both impls together.

```
go test ./internal/blocklist/ -run TestLoader_MixesFileAndHTTPSources -v
```

Expected: PASS (no production code change yet). If it fails, the issue is in one of the prior tasks; fix there.

- [ ] **Step 3: Update `cmd/bns/serve.go` source construction**

Replace the existing block (lines ~129–135) that hard-codes `s.Type != "file"`:

```go
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
```

(Validation has already rejected unknown types; the default branch is defence-in-depth.)

- [ ] **Step 4: Build to confirm**

```
go build ./...
```

Expected: success.

- [ ] **Step 5: Commit**

```bash
git add cmd/bns/serve.go internal/blocklist/loader_test.go
git commit -m "blocklist: wire http source into loader (spec §5.2)"
```

---

## Task 10: Metrics — fetch counter + last-success gauge + per-source entries gauge

**Files:**
- Modify: `internal/metrics/metrics.go`
- Modify: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Write the failing test**

Append a test asserting the three new metric families are registered and labelled:

```go
func TestMetrics_BlocklistFetchSurface(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)

	m.BlocklistFetchTotal.WithLabelValues("hagezi-pro", "success").Inc()
	m.BlocklistLastSuccessTimestamp.WithLabelValues("hagezi-pro").Set(123)
	m.BlocklistEntriesBySource.WithLabelValues("hagezi-pro").Set(10000)

	got, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, mf := range got {
		names[mf.GetName()] = true
	}
	require.True(t, names["bns_blocklist_fetch_total"])
	require.True(t, names["bns_blocklist_last_success_timestamp_seconds"])
	require.True(t, names["bns_blocklist_entries_by_source"])
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./internal/metrics/ -run TestMetrics_BlocklistFetchSurface -v
```

Expected: FAIL (fields undefined).

- [ ] **Step 3: Add metrics**

Append to the `Metrics` struct:

```go
BlocklistFetchTotal           *prometheus.CounterVec
BlocklistLastSuccessTimestamp *prometheus.GaugeVec
BlocklistEntriesBySource      *prometheus.GaugeVec
```

In `New`, after the existing `BlocklistReloadsTotal` init:

```go
BlocklistFetchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_blocklist_fetch_total",
    Help: "HTTP blocklist fetches, by source and outcome (success|not_modified|failure).",
}, []string{"source", "outcome"}),
BlocklistLastSuccessTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
    Name: "bns_blocklist_last_success_timestamp_seconds",
    Help: "Unix time of the last successful fetch (success OR not_modified) per source.",
}, []string{"source"}),
BlocklistEntriesBySource: prometheus.NewGaugeVec(prometheus.GaugeOpts{
    Name: "bns_blocklist_entries_by_source",
    Help: "Parsed entry count per source after the most recent successful fetch.",
}, []string{"source"}),
```

Add them to the `reg.MustRegister(...)` call.

Add an adapter helper that returns a `blocklist.FetcherMetrics` from `*Metrics`:

```go
// BlocklistFetcherMetrics returns the adapter wiring the Fetcher's
// metric hooks into this Metrics bundle.
func (m *Metrics) BlocklistFetcherMetrics() blocklist.FetcherMetrics {
    return blocklist.FetcherMetrics{
        IncFetch: func(source string, outcome blocklist.FetchOutcome) {
            m.BlocklistFetchTotal.WithLabelValues(source, string(outcome)).Inc()
        },
        SetLastSuccess: func(source string, ts time.Time) {
            m.BlocklistLastSuccessTimestamp.WithLabelValues(source).Set(float64(ts.Unix()))
        },
        SetEntries: func(source string, n int) {
            m.BlocklistEntriesBySource.WithLabelValues(source).Set(float64(n))
        },
    }
}
```

Add `"time"` and `"github.com/bcrisp4/bns/internal/blocklist"` to imports. If this creates an import cycle (it should not — blocklist does not import metrics), restructure by moving `FetcherMetrics` into a separate trivial package or pass three function fields explicitly from `serve.go`.

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./internal/metrics/ -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/metrics.go internal/metrics/metrics_test.go
git commit -m "metrics: blocklist fetch counter + gauges (spec §7.1)"
```

---

## Task 11: serve.go — wire Fetcher into errgroup + fold SIGHUP

**Files:**
- Modify: `cmd/bns/serve.go`

This task has no unit test of its own; correctness is verified by Task 13's end-to-end integration test. Keep changes minimal.

- [ ] **Step 1: Drop the `--blocklist` CLI flag**

In `newServeCmd`, delete:

```go
cmd.Flags().StringSlice("blocklist", nil, "Blocklist file path; repeat for multiple (default none)")
```

In `bindServeFlags`, delete the `setSliceFlag(..., "blocklist", ...)` call.

- [ ] **Step 2: Build the Fetcher and wire it**

Replace the `SIGHUP` goroutine (lines ~224–250) with a version that:
1. Constructs the `blocklist.Fetcher` from the resolved upstream addrs + cache_dir.
2. Starts `Fetcher.Run` in the errgroup.
3. SIGHUP calls `loader.Load` (reload from disk for file-source edits) AND `fetcher.RefreshNow` (kick a network cycle).

**Construction (insert after `loader := blocklist.NewLoader(sources)` and BEFORE the errgroup):**

```go
// Bootstrap resolver for the fetcher: dial configured upstream IPs
// directly, bypassing the system stub (which may point at this very
// BNS process when BNS is the LAN's sole resolver).
upstreamAddrs := make([]string, 0, len(cfg.Upstreams))
for _, u := range cfg.Upstreams {
    upstreamAddrs = append(upstreamAddrs, u.Addr)
}
bootstrapResolver := blocklist.NewBootstrapResolver(upstreamAddrs)
httpClient := &http.Client{
    Timeout: 60 * time.Second,
    Transport: &http.Transport{
        Proxy: http.ProxyFromEnvironment,
        DialContext: (&net.Dialer{
            Timeout:  10 * time.Second,
            Resolver: bootstrapResolver,
        }).DialContext,
        MaxIdleConns:        10,
        IdleConnTimeout:     90 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
    },
}

targets := make([]blocklist.FetchTarget, 0)
for _, s := range cfg.Blocklists.Sources {
    if s.Type == "http" {
        targets = append(targets, blocklist.FetchTarget{Name: s.Name, URL: s.URL})
    }
}

// userAgent: if no version helper exists yet, use "bns/dev"; bump when
// ldflags version wiring lands.
fetcher := blocklist.NewFetcher(blocklist.FetcherConfig{
    Store:     store, // from Task 9
    Client:    httpClient,
    Interval:  cfg.Blocklists.RefreshInterval,
    Logger:    logger,
    Metrics:   mtr.BlocklistFetcherMetrics(),
    UserAgent: "bns/dev (+https://github.com/bcrisp4/bns)",
})
```

**Errgroup wiring (replaces the existing SIGHUP goroutine, AFTER `g, gctx := errgroup.WithContext(ctx)`):**

The `reloadFromDisk` closure must be defined inside this block because it captures `gctx`.

```go
reloadFromDisk := func() {
    rctx, cancel := context.WithTimeout(gctx, 30*time.Second)
    defer cancel()
    next, rcount, rerr := loader.Load(rctx)
    if rerr != nil {
        logger.Error("blocklist reload failed", "err", rerr)
        mtr.BlocklistReloadsTotal.WithLabelValues("error").Inc()
        return
    }
    holder.Swap(next)
    mtr.BlocklistEntries.Set(float64(next.Size()))
    mtr.BlocklistLoadedTimestamp.Set(float64(time.Now().Unix()))
    mtr.BlocklistReloadsTotal.WithLabelValues("ok").Inc()
    logger.Info("blocklist reloaded", "raw", rcount, "unique", next.Size())
}

g.Go(func() error {
    return fetcher.Run(gctx, targets, reloadFromDisk)
})
g.Go(func() error {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGHUP)
    defer signal.Stop(ch)
    for {
        select {
        case <-gctx.Done():
            return nil
        case <-ch:
            logger.Info("SIGHUP received, reloading blocklist + kicking fetcher")
            reloadFromDisk()
            fetcher.RefreshNow()
        }
    }
})
```

Add `"net/http"` and `"net"` to imports if not already present (`net` is — confirm).

- [ ] **Step 3: Build**

```
go build ./...
```

Expected: success.

- [ ] **Step 4: Run all tests including the existing serve-level test**

```
go test ./... -race
```

Expected: PASS. If any test depended on `--blocklist` CLI flag, update it to use a YAML config file via `t.TempDir()`.

- [ ] **Step 5: Commit**

```bash
git add cmd/bns/serve.go
git commit -m "serve: wire Fetcher + drop --blocklist flag (spec §5.3)"
```

---

## Task 12: Examples + container config

**Files:**
- Modify: `examples/config.example.yaml`
- Modify: `deploy/docker/config.yaml`

- [ ] **Step 1: Rewrite `examples/config.example.yaml`** blocklists block to demonstrate both source types and the new global keys:

```yaml
blocklists:
  refresh_interval: 24h
  cache_dir: /var/cache/bns/blocklists
  sources:
    - type: http
      name: hagezi-pro
      url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/pro.txt
    - type: file
      name: custom-blocklist
      path: ./examples/sample-blocklist.txt
```

- [ ] **Step 2: Rewrite `deploy/docker/config.yaml`** blocklists block:

```yaml
blocklists:
  refresh_interval: 24h
  cache_dir: /var/cache/bns/blocklists
  sources:
    - type: http
      name: hagezi-pro
      url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/pro.txt
```

(Drop the previous baked `type: file` entry pointing at `/etc/bns/blocklists/pro.txt`.)

- [ ] **Step 3: Smoke build with the new example**

```
go build -o bin/bns ./cmd/bns
./bin/bns serve -c examples/config.example.yaml --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354 --upstream 1.1.1.1:53 &
sleep 2
curl -s http://127.0.0.1:9090/metrics | grep bns_blocklist_fetch_total
kill %1
wait 2>/dev/null
```

Expected: at least one `bns_blocklist_fetch_total{...}` line. (Requires outbound HTTPS — skip on air-gapped dev hosts.)

- [ ] **Step 4: Commit**

```bash
git add examples/config.example.yaml deploy/docker/config.yaml
git commit -m "examples: hagezi http source in default configs"
```

---

## Task 13: Integration test — end-to-end fetch + block

**Files:**
- Create: `internal/integration/http_blocklist_test.go`

- [ ] **Step 1: Write the failing test**

```go
package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/admin"
	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/bcrisp4/bns/internal/cache"
	"github.com/bcrisp4/bns/internal/config"
	"github.com/bcrisp4/bns/internal/logging"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/resolver/chain"
	"github.com/bcrisp4/bns/internal/upstream"
	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestHTTPBlocklistSource_FetchThenBlock asserts that a cold-start BNS
// with only an HTTP source: (1) initially does not block (cache empty),
// (2) fetches the body from the test server, (3) after the fetcher
// triggers reload, blocks the listed domain.
func TestHTTPBlocklistSource_FetchThenBlock(t *testing.T) {
	// Test HTTP server serving a single blocked domain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("blocked.example\n"))
	}))
	t.Cleanup(srv.Close)

	cacheDir := filepath.Join(t.TempDir(), "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))

	// Synthetic upstream answering everything with NOERROR + 127.0.0.1.
	upstreamAddr := testutil.SpawnEchoUpstream(t)

	// Build the resolver chain manually (mirrors serve.go but without
	// listeners or signal handlers — we exercise the chain directly).
	reg := prometheus.NewRegistry()
	mtr := metrics.New(reg)
	logger := logging.New(config.Logging{Level: "info", Format: "text"}, os.Stderr)

	store := blocklist.NewCacheStore(cacheDir)
	httpSource := blocklist.NewHTTPSource("test", srv.URL, store)
	loader := blocklist.NewLoader([]blocklist.Source{httpSource})
	initial, _, err := loader.Load(context.Background())
	require.NoError(t, err)
	holder := blocklist.NewHolder(initial)

	pool := upstream.NewPool(
		[]upstream.Upstream{upstream.NewUDPClient(upstreamAddr, time.Second)},
		[]string{upstreamAddr},
		mtr,
	)

	chainResolver := chain.Build(chain.Deps{
		Upstream: pool, Cache: cache.NewLRU(100),
		CacheCfg: config.Cache{MaxTTL: time.Hour, NegativeTTLMax: time.Minute},
		Blocklist: holder,
		QueryLog: logging.QueryLogger(config.QueryLog{}, os.Stderr),
		Metrics: mtr,
	})

	// Before fetch: blocked.example is NOT blocked.
	res, err := chainResolver.Resolve(context.Background(), dns.NewMsg("blocked.example.", dns.TypeA))
	require.NoError(t, err)
	require.NotEqual(t, uint16(dns.RcodeNameError), res.Rcode)

	// Run fetcher; reload swaps the matcher.
	fetcher := blocklist.NewFetcher(blocklist.FetcherConfig{
		Store: store, Client: srv.Client(), Interval: time.Hour, Logger: logger,
		Metrics: mtr.BlocklistFetcherMetrics(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		_ = fetcher.Run(ctx, []blocklist.FetchTarget{{Name: "test", URL: srv.URL}}, func() {
			next, _, _ := loader.Load(context.Background())
			holder.Swap(next)
		})
		close(done)
	}()

	require.Eventually(t, func() bool {
		res, err := chainResolver.Resolve(context.Background(), dns.NewMsg("blocked.example.", dns.TypeA))
		return err == nil && res.Rcode == uint16(dns.RcodeNameError)
	}, 3*time.Second, 20*time.Millisecond)

	cancel()
	<-done

	// Silence the linter on unused admin import; it's there to match
	// the production wiring shape.
	_ = admin.New
	_ = resolver.NewHandler
}
```

If `testutil.SpawnEchoUpstream` does not exist, add it to `internal/upstream/testutil/` alongside the existing `SpawnServer` helper. It should answer A queries with `127.0.0.1` and pass through everything else.

- [ ] **Step 2: Run the test**

```
go test ./internal/integration/ -run TestHTTPBlocklistSource_FetchThenBlock -v -race
```

Expected: PASS. If the chain integration helper differs from what's shown, simplify the test to drive the chain via the smallest viable Deps — the goal is to prove "fetcher writes cache → loader reload → matcher swap → block stage NXDOMAINs."

- [ ] **Step 3: Commit**

```bash
git add internal/integration/http_blocklist_test.go internal/upstream/testutil/*.go
git commit -m "integration: cold-start http fetch then block (spec §9)"
```

---

## Task 14: Docker — drop baked list, add cache volume

**Files:**
- Modify: `deploy/docker/Dockerfile`

- [ ] **Step 1: Edit the Dockerfile**

Remove the `hagezi-fetch` stage entirely (the FROM ... AS hagezi-fetch block plus its curl invocation and ARG HAGEZI_TAG). Remove the `COPY --from=hagezi-fetch ...` line from the runtime stage. Remove `ARG HAGEZI_TAG`.

In the runtime stage add a writable cache directory owned by uid 65532. Distroless cannot run `chown`; create the dir in a temporary stage and copy it with ownership flags, or use `--chown` on a `COPY` of an empty scratch directory. Concrete approach:

```dockerfile
# In a prior builder stage (alongside the go build stage), create an empty dir:
RUN mkdir -p /out/var/cache/bns

# In the runtime stage:
COPY --from=builder --chown=65532:65532 /out/var/cache/bns /var/cache/bns
VOLUME /var/cache/bns
```

If the existing builder stage doesn't permit a `mkdir /out/...`, add a tiny intermediate `FROM busybox AS cachedir RUN mkdir -p /out/var/cache/bns` stage and copy from it.

- [ ] **Step 2: Build the image (single arch sanity)**

```
docker buildx build --platform linux/arm64 -f deploy/docker/Dockerfile -t bns:test --load .
```

Expected: success. (Per CLAUDE.md, `--load` is single-arch only.)

- [ ] **Step 3: Smoke run the container**

```
docker run --rm -d --name bns-smoke -p 15354:5354/udp -p 19090:9090/tcp bns:test
sleep 5
dig @127.0.0.1 -p 15354 example.com +tries=1 +time=2
curl -s http://127.0.0.1:19090/metrics | grep bns_blocklist_fetch_total
docker rm -f bns-smoke
```

Expected: dig returns an answer (forwarded); metrics show a `bns_blocklist_fetch_total{source="hagezi-pro",outcome="success"}` line ≥ 1. If outbound HTTPS is unavailable, success may be `failure`; that's fine — the metric line presence is what matters.

- [ ] **Step 4: Commit**

```bash
git add deploy/docker/Dockerfile
git commit -m "docker: drop baked blocklist, mount /var/cache/bns"
```

---

## Task 15: Documentation — README + CLAUDE.md

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update `README.md`**

Add a "Blocklists" subsection (location: after the existing config / quickstart sections). Cover:
- New YAML keys: `blocklists.refresh_interval`, `blocklists.cache_dir`.
- Required `name` field per source.
- Two supported types: `file`, `http`.
- HTTP source: polite conditional GET, on-disk cache, bootstrap resolver.
- Fail-open behaviour.
- Cold-start UX (matcher empty until first fetch lands).
- New metrics family: `bns_blocklist_fetch_total`, `bns_blocklist_last_success_timestamp_seconds`, `bns_blocklist_entries_by_source`.
- SIGHUP behaviour (reload disk + kick fetcher).
- Removal of the `--blocklist` CLI flag.
- Container volume mount example: `-v bns-cache:/var/cache/bns`.

- [ ] **Step 2: Update `CLAUDE.md`**

Tighten the existing blocklist section. Add gotchas:
- HTTPSource.Load is disk-only; never makes network calls.
- Fetcher is the only network owner; lives under `internal/blocklist/http_fetcher.go`.
- `BlocklistSource.Name` is required; viper unmarshals an empty `name` as `""` — validation rejects.
- `cache_dir` must be writable by uid 65532 in container.
- Orphan sweep runs once at startup only; mid-runtime source removal leaves files until next restart.
- Bootstrap dialer uses upstream IPs only; it does NOT go through the BNS resolver chain (would deadlock).
- For tests, use `httptest.NewServer` + `t.TempDir`; never hit the real internet.

- [ ] **Step 3: Commit**

```bash
git add README.md CLAUDE.md
git commit -m "docs: http blocklist source + cache layout"
```

---

## Task 16: docs/TODO.md — append deferred items

**Files:**
- Modify: `docs/TODO.md`

- [ ] **Step 1: Append a new section** under the existing TODO structure:

```markdown
## Blocklist source extensions (2026-05-21-bns-http-blocklist-source)

- **Allowlist / per-domain override.** Curated lists like hagezi false-positive on legit domains. Add an `allowlist:` block whose matches short-circuit the block stage before the matcher.
- **Tiered hagezi flavour selection.** `light` / `multi` / `pro` / `pro.plus` / `ultimate`. Single config key that resolves to the right URL; saves operators from copy-pasting URLs.
- **Integrity / sha256 verification.** Hagezi publishes `.sha256` siblings. Optional `sha256_url:` per source; fetcher refuses to swap cache if hash mismatches.
- **Per-source refresh intervals.** Today's global `blocklists.refresh_interval` is fine when one URL dominates. Add `sources[].refresh_interval:` override when a second URL has different freshness needs.
- **Configurable failure-mode policy.** Today: always fail-open. Add `on_failure: {fail_open, fail_start, fail_after: 7d}` per source for stricter operators.
- **Force-refresh admin HTTP endpoint.** `POST /admin/refresh-blocklists` so operators can poke without a SIGHUP capability. Needs admin auth design first.
- **Source types beyond file/http.** FTP, S3, gs://, git. Each implements `Source` + (for fetched sources) provides its own fetcher; the `type:` discriminator already supports it.
- **Fetch latency histogram metric.** `bns_blocklist_fetch_duration_seconds` when fetch perf becomes interesting.
- **Default-list curation.** Revisit which list ships in the default container config (currently hagezi pro). Consider shipping a slim "ads + trackers only" list as the default and documenting pro as an opt-in.
```

- [ ] **Step 2: Commit**

```bash
git add docs/TODO.md
git commit -m "docs: TODO entries deferred from http-blocklist plan"
```

---

## Final verification

After all tasks:

- [ ] `make race` passes
- [ ] `make vet` passes
- [ ] `./bin/bns serve -c examples/config.example.yaml --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354 --upstream 1.1.1.1:53` starts cleanly
- [ ] `curl -s http://127.0.0.1:9090/metrics | grep bns_blocklist_fetch_total` shows at least one line within ~30s
- [ ] `dig @127.0.0.1 -p 5354 google.com` returns a normal answer
- [ ] After fetch lands: `dig @127.0.0.1 -p 5354 <a-known-hagezi-blocked-domain>` returns NXDOMAIN
- [ ] `kill -HUP $(pgrep -f bin/bns)` triggers an INFO log AND a `not_modified` fetch outcome
- [ ] Docker container build + smoke matches Task 14 expectations
- [ ] `/simplify` run (per global CLAUDE.md) once tasks complete
