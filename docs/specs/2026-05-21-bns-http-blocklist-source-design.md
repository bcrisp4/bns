# BNS HTTP Blocklist Source — Design Spec

- **Date:** 2026-05-21
- **Status:** Draft (pending implementation plan)
- **Author:** Ben Crisp
- **Builds on:** `docs/specs/2026-05-19-bns-mvp-design.md`

## 1. Context

BNS MVP ships with one mechanism for sourcing blocklists: local files on
disk, loaded at startup and reloaded on `SIGHUP`. The container image bakes
a snapshot of hagezi's `pro.txt` at build time, pinned via a `HAGEZI_TAG`
build argument. To refresh, the image must be rebuilt and redeployed.

This is sufficient for a v0.1 deployment but has two operational gaps:

1. Well-known blocklists (hagezi, OISD, StevenBlack) update more frequently
   than BNS releases. Operators currently have no automatic refresh path
   short of rebuilding the container or manually mirroring lists to disk.
2. The on-disk file source serves the operator-controlled custom-list case
   well, but there is no first-class way to point BNS at an upstream URL.

This spec adds an HTTP blocklist source with automatic polite refresh,
basic observability, and on-disk persistence, intentionally narrow in
scope. Adjacent concerns (allowlists, integrity verification, per-source
cadence, tiered list selection) are deferred to `docs/TODO.md`.

## 2. Goals

1. Operators can declare blocklist sources fetched over HTTP(S) in YAML
   configuration.
2. Sources auto-refresh on a global interval without process restart and
   without `SIGHUP`.
3. Refresh is polite: conditional `GET` (ETag / `If-Modified-Since`) skips
   the body when the upstream is unchanged.
4. Fetched bodies are cached to disk so restarts and short outages do not
   re-download.
5. Failures are non-fatal: BNS never refuses to serve DNS because a
   blocklist fetch failed.
6. Per-source observability via Prometheus metrics and structured logs is
   sufficient to alert on stale or failing sources.
7. BNS can fetch its own blocklists even when it is the sole resolver on
   the network (no chicken-and-egg).

## 3. Non-goals (this spec)

Deferred to `docs/TODO.md`:

- Allowlist / override list (blocklist domain unblocking).
- Tiered hagezi flavour selection (`light` / `multi` / `pro` / `ultimate`).
- Integrity verification (sha256 sidecar checking).
- Per-source refresh intervals (single global interval only).
- Configurable failure-mode policy (always fail-open).
- Histogram of fetch latency (counter + gauges only).
- Admin endpoint for force-refresh (`SIGHUP` already triggers refresh).
- Source types beyond `file` and `http` (FTP, S3, etc).

## 4. Configuration schema

`internal/config` gains three new keys under the existing `blocklists`
block. `name` is **required** on every source; configuration validation
fails fast if missing.

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
      path: /etc/bns/custom-blocklist.txt
```

Defaults if `blocklists` is omitted entirely: no sources, empty matcher
(unchanged from MVP). Defaults for present-but-incomplete `blocklists`
block:

- `refresh_interval`: `24h`
- `cache_dir`: `/var/cache/bns/blocklists`

Validation rules:

- Every source has `name` and `type`. Names must be unique across sources.
- `file` sources require `path`; `http` sources require `url`.
- `url` must parse and have scheme `http` or `https`.
- `refresh_interval` must be ≥ 1 minute (sanity guard).
- `cache_dir` must be writable at startup (lazy: failure deferred to first
  fetch and reported via metric + log; does not block startup).

The CLI `--blocklist` flag is **removed**. The flag previously took a file
path and is operator-facing only; with `name` required and scheme-dispatched
references, a CLI form becomes noisy. Operators use YAML or `-c
/tmp/test.yaml` for one-off overrides.

Environment variables remain ineffective for the `sources` slice (viper
slice-env gotcha documented in `CLAUDE.md`). `refresh_interval` and
`cache_dir` can be set via env (`BNS_BLOCKLISTS__REFRESH_INTERVAL`,
`BNS_BLOCKLISTS__CACHE_DIR`).

## 5. Architecture

### 5.1 Package layout

```
internal/blocklist/
  source.go              # existing Source interface, unchanged
  file_source.go         # existing
  http_source.go         # NEW: Source impl backed by on-disk cache only
  http_fetcher.go        # NEW: background goroutine, owns HTTP client + ticker
  cache_store.go         # NEW: atomic read/write for body + .meta.json
  bootstrap_dialer.go    # NEW: net.Resolver that dials configured upstream IPs
  parse.go               # existing
  matcher.go             # existing
  holder.go              # existing
  loader.go              # existing; gains HTTPSource alongside FileSource
```

### 5.2 Separation of concerns

`HTTPSource.Load()` performs **no network I/O**. It reads the cached body
from disk and returns parsed entries. Network fetching lives entirely in
`Fetcher`, a separate goroutine. This split:

- Keeps `Loader.Load` synchronous, fast, and deterministic regardless of
  source mix.
- Makes startup, `SIGHUP` reload, and `Fetcher`-triggered reload share one
  code path.
- Ensures the request-serving path never blocks on a network fetch.

### 5.3 Lifecycle

Startup sequence in `cmd/bns/serve.go:runServe`:

1. `config.Load`.
2. `blocklist.Loader.Load` reads file sources from their configured paths
   and HTTP sources from `cache_dir`. HTTP sources with no cache file
   produce zero entries and a `WARN` log.
3. `holder.Swap(matcher)` — serving begins, possibly with an empty or
   partial matcher.
4. UDP/TCP listeners + admin HTTP up.
5. `fetcher.Run(ctx)` enters the `errgroup`. First tick fires immediately,
   in the background. Serving is never gated on it.
6. `SIGHUP` handler enters the `errgroup`.

Refresh cycle inside `Fetcher`:

```text
select {
  case <-ticker.C:           // global refresh_interval
  case <-refreshNow:         // SIGHUP poke
  case <-ctx.Done(): return
}

wroteNewBody := false
for each http source:
  body, etag, lastMod, ok := fetch(source)
  if ok && body != nil { wroteNewBody = true }

if wroteNewBody:
  loader.Reload()           // re-read ALL sources from disk; atomic Swap
```

`Fetcher` does **not** swap the matcher directly. After any successful
fetch with a new body, it calls `loader.Reload()`, which re-reads every
source from disk (files + HTTP caches) and builds a fresh matcher. One
code path, one matcher snapshot, no per-source matcher state.

`SIGHUP` is wired to both:
- `loader.Reload()` directly (re-read disk; picks up operator edits to file
  sources without waiting for next fetch tick).
- `fetcher.refreshNow` channel (kicks an immediate HTTP fetch cycle).

Shutdown: `Fetcher` honors `ctx.Done()`. Mid-fetch HTTP requests cancel via
the request context. Any `<sha256>.txt.tmp` left behind is swept by
`Fetcher` on next startup.

### 5.4 HTTP client

Hardcoded defaults for v1 (no config knobs):

| Field             | Value                                               |
| ----------------- | --------------------------------------------------- |
| Per-fetch timeout | 60s                                                 |
| Idle conn timeout | 90s (stdlib default)                                |
| Redirect policy   | Follow, max 5 hops                                  |
| Max body size     | 64 MiB (sanity guard; hagezi ultimate ~10 MiB)      |
| Accept-Encoding   | `gzip`                                              |
| User-Agent        | `bns/<version> (+https://github.com/bcrisp4/bns)`   |
| TLS verification  | Standard (system CA bundle); no pinning             |

Conditional GET:

- Send `If-None-Match` and `If-Modified-Since` headers from the sidecar
  meta when present.
- `304 Not Modified` → no body read, `not_modified` outcome, cache untouched.
- `200 OK` → stream to `<sha256(url)>.txt.tmp` under `cache_dir`, validate
  with `Parse` (reject if parse yields zero entries from non-empty body),
  fsync, `os.Rename` to `<sha256(url)>.txt`. Write `<sha256(url)>.meta.json`
  similarly. Atomic rename guarantees readers see complete files only.
- Any other status, network error, parse failure → `failure` outcome, cache
  untouched, `WARN` log, no matcher reload.

### 5.5 Bootstrap dialer

When BNS is the sole resolver on a LAN, `raw.githubusercontent.com` cannot
be resolved via the host's stub resolver — that would point back at BNS
itself, which has not finished starting.

`bootstrap_dialer.go` provides a custom `net.Resolver` (`PreferGo: true`)
whose `Dial` function dials the IPs from `upstream.Pool` directly on the
requested network (`udp` or `tcp`) at port 53. The stdlib pure-Go resolver
handles DNS framing and TCP fallback on truncation; the dialer's only job
is to supply a connection to an upstream of our choosing. Crucially, it
uses the upstream pool's **IP set only** — it does **not** route DNS
resolution through the BNS resolver chain (which would deadlock).

The fetcher's `http.Transport.DialContext` uses an inner `net.Dialer`
whose `Resolver` field is set to this custom resolver. TLS still validates
against the system CA bundle.

### 5.6 On-disk cache layout

Under `cache_dir`:

```
<sha256(url)>.txt          # raw fetched body, parseable directly
<sha256(url)>.meta.json    # { etag, last_modified, fetched_at, url, bytes, entries }
```

Filename keyed on `sha256(url)` (deterministic, survives `name` field
changes). The sidecar persists fetcher state separately from the list
body so the body file remains directly inspectable (`cat`, `wc -l`) and
the format-versioning surface stays small.

Container concern: `/var/cache/bns` needs writable storage; distroless
nonroot uid 65532 must own it. Addressed in §8.

## 6. Failure handling

Always fail-open per Q3 in brainstorming. The matrix:

| State | Scenario                                          | Behavior                                                                                                       |
| ----- | ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| S1    | Cold start, no on-disk cache, fetch fails         | Serve empty for that source. WARN log. Retry on next tick.                                                     |
| S2    | Cold start, cache exists, fetch fails             | Serve from cache. INFO on load, WARN on fetch.                                                                 |
| S3    | Running, refresh fails                            | Keep previous matcher. WARN log. `failure` counter increments. `last_success_timestamp` unchanged → alertable. |
| S4    | Stale cache + fetch fails                         | Same as S3. No staleness threshold in v1. Operator alerts on `time() - last_success > N`.                      |

BNS never refuses to start because of a blocklist source. The "no
blocking" failure mode is loud (WARN with reason) but not fatal.

## 7. Observability

### 7.1 Metrics

All blocklist metrics labelled by `source` (the operator-supplied `name`).

| Metric                                                   | Type    | Description                                  |
| -------------------------------------------------------- | ------- | -------------------------------------------- |
| `bns_blocklist_fetch_total{source,outcome}`              | Counter | `outcome` ∈ {`success`, `not_modified`, `failure`} |
| `bns_blocklist_last_success_timestamp_seconds{source}`   | Gauge   | Unix seconds; for `time() - this` staleness alerts |
| `bns_blocklist_entries{source}`                          | Gauge   | Parsed entry count per source                |

No fetch latency histogram in v1 (deferred; YAGNI for ~1/day operation).

### 7.2 Logs

Structured `slog` records via the existing logger.

- One `INFO` per successful fetch:
  `source`, `outcome`, `bytes`, `entries`, `duration_ms`, `etag_hit` (bool),
  `status_code`.
- One `WARN` per failed fetch:
  `source`, `error`, `status_code` (if applicable), `duration_ms`.
- One `INFO` on matcher swap with total entry count and per-source
  contribution.

## 8. Deploy and container changes

`deploy/docker/Dockerfile`:

- Drop the `hagezi-fetch` build stage entirely.
- Drop the `HAGEZI_TAG` build argument.
- Drop `COPY` of `/etc/bns/blocklists/pro.txt` into runtime image.
- Add `VOLUME /var/cache/bns`.
- Ensure `/var/cache/bns` exists and is owned by uid 65532 in the runtime
  stage (`COPY --chown=65532:65532` of an empty scratch dir, or
  equivalent).

`deploy/docker/config.yaml`:

```yaml
blocklists:
  refresh_interval: 24h
  cache_dir: /var/cache/bns/blocklists
  sources:
    - type: http
      name: hagezi-pro
      url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/domains/pro.txt
```

First-run UX: container starts with empty matcher, serves DNS, fetches in
the background, swaps matcher in once `pro.txt` lands (~seconds on a
healthy link). A `WARN` log on the no-cache cold start makes the
unblocked window observable.

README and `CLAUDE.md`:

- Document new `blocklists.refresh_interval` and `blocklists.cache_dir`
  keys.
- Document required `name` on every source.
- Document removal of `--blocklist` CLI flag.
- Document the `bns_blocklist_*` metric family.
- Document the volume mount example for persistent cache across container
  restarts.
- Note: container image no longer carries a baked blocklist; first start
  needs network to be useful.

## 9. Testing

TDD throughout, race-clean (`make race`).

| Unit                  | Approach                                                                                                                                                                                                                                          |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `cache_store`         | tempdir; write/read round-trip for body + meta; simulate crash mid-write by leaving `.tmp` and verify reader picks previous version; sweep behavior on startup.                                                                                    |
| `http_source`         | tempdir cache; returns parsed entries from a planted body file; returns zero entries + nil error when no cache file present.                                                                                                                       |
| `http_fetcher`        | `httptest.Server` upstream. First call 200 → cache + meta written. Second call: verify `If-None-Match` header sent, respond 304 → no cache change. 500 response → cache untouched + `failure` counter. Inject fake clock for ticker.               |
| `bootstrap_dialer`    | Boot in-process `dns.Server` via existing `internal/upstream/testutil`. Custom resolver resolves a test name to loopback. Verify the inner dialer routes via this resolver and not the host stub.                                                  |
| Integration (`internal/integration`) | End-to-end. `httptest.Server` serves a blocklist body. Boot BNS cold (empty cache). Assert: first `dig` is unblocked. Wait for fetcher (or poll `bns_blocklist_fetch_total{outcome="success"}` > 0). Assert: same `dig` now NXDOMAIN.   |

No network in tests. Everything via `httptest.NewServer`, injected
resolver, and injected clock. Tests stay offline and race-clean.

## 10. Out of scope (deferred to `docs/TODO.md`)

- Allowlist / per-domain override.
- Tiered hagezi flavour selection.
- Integrity / sha256 verification.
- Per-source refresh intervals.
- Configurable failure-mode policy (fail-start, stale thresholds).
- Force-refresh admin HTTP endpoint.
- Source types beyond `file` and `http`.
- Fetch latency histogram metric.
- Default-list curation (which list, how many) beyond "hagezi pro by default".
