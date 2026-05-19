# BNS MVP — Design Spec

- **Date:** 2026-05-19
- **Status:** Draft (pending implementation plan)
- **Author:** Ben Crisp
- **Implements:** `docs/prompts/seed.md`

## 1. Context

BNS (Ben's Name Server) is a caching DNS forwarder with ad-blocking, intended
for a small private network and deployed on Raspberry Pi-class hardware. It
fills the same role as Pi-hole or AdGuard Home but is intentionally minimal:
single Go binary, no UI, no admin API, configured via YAML/env/flags.

This document captures the design for the MVP. Out-of-scope extensions are
listed in §10 and are explicitly deferred.

## 2. Goals

1. Accept and answer DNS queries over UDP and TCP.
2. Forward unanswerable queries to configured upstream DNS servers.
3. Block known ad/tracker domains via local blocklist files.
4. Cache responses in memory with TTL-aware bounded eviction.
5. Coalesce concurrent identical upstream queries.
6. Expose Prometheus metrics and Kubernetes-style health probes.
7. Be configurable via CLI flags, environment variables, and a YAML file
   (in that order of precedence).
8. Be thread-safe under concurrent load.
9. Reload blocklists on `SIGHUP` without dropping in-flight queries.

Performance target: a few thousand QPS on a Raspberry Pi 4 / 5. Not a
high-scale resolver.

## 3. Non-goals (MVP)

- Persistent cache across restarts.
- DNS-over-HTTPS / DNS-over-TLS / DNS-over-QUIC upstream protocols (design
  must not preclude them, but no implementation).
- DNSSEC validation.
- Allowlist / per-client policy / per-client logging.
- Remote blocklist fetching with periodic refresh (the loader is behind an
  interface so this can be added later without churn).
- A web UI or admin API.
- Container images, systemd unit files, packaging beyond a static binary,
  an example YAML config, and a sample blocklist.

## 4. High-level architecture

Single binary. `cmd/bns/main.go` parses configuration, builds the dependency
graph, wires the resolver chain, and starts the listeners.

### Package layout

```
cmd/bns/                          // main, signal handling, wire-up

internal/config/                  // viper schema, defaults, validation
internal/logging/                 // slog setup, query-log gate
internal/server/                  // UDP + TCP listeners (codeberg miekg/dns)
internal/resolver/                // Resolver interface + chain builder
internal/resolver/blocklist/      // blocklist stage
internal/resolver/cache/          // cache stage
internal/resolver/coalesce/       // dedup stage (today backed by singleflight)
internal/resolver/forward/        // upstream forwarder stage (last in chain)
internal/blocklist/               // Source interface, FileSource, Matcher
internal/cache/                   // LRU cache with TTL
internal/upstream/                // Upstream interface, UDP/TCP client, pool
internal/metrics/                 // prometheus registry, collectors
internal/health/                  // /healthz, /readyz handlers
internal/admin/                   // single http.Server hosting admin endpoints
```

All real code lives under `internal/` so nothing is importable from outside
the module.

### Extensibility seams (interfaces)

The MVP defines three interfaces with single concrete implementations. They
exist so the obvious extensions can be added without touching consumers.

| Interface           | Package                 | MVP impl       | Future impls                       |
| ------------------- | ----------------------- | -------------- | ---------------------------------- |
| `Resolver`          | `internal/resolver`     | each stage     | DNSSEC validator, ECS, rewrites    |
| `Upstream`          | `internal/upstream`     | `UDPClient`    | `DoHClient`, `DoTClient`, `DoQClient` |
| `blocklist.Source`  | `internal/blocklist`    | `FileSource`   | `URLSource`, `HostsURLSource`, …   |

### External dependencies

| Purpose       | Library                                   | Notes                                                                                              |
| ------------- | ----------------------------------------- | -------------------------------------------------------------------------------------------------- |
| DNS protocol  | `codeberg.org/miekg/dns` (v2)             | **Import path has no `/v2` suffix** despite being v2. NOT `github.com/miekg/dns` (v1, unmaintained). |
| Metrics       | `github.com/prometheus/client_golang`     | `promhttp.Handler()` for `/metrics`.                                                               |
| CLI           | `github.com/spf13/cobra`                  | Single root command.                                                                               |
| Config        | `github.com/spf13/viper`                  | YAML + env (`BNS_` prefix) + flags.                                                                |
| Coalescing    | `golang.org/x/sync/singleflight`          | Implementation detail of `internal/resolver/coalesce`; not exposed in API.                         |
| Logging       | stdlib `log/slog`                         | JSON handler.                                                                                      |
| Test asserts  | `github.com/stretchr/testify/require`     | Already-ubiquitous, keeps test code tight.                                                         |

LRU cache is implemented in-tree rather than depending on
`hashicorp/golang-lru` — it is ~80 LoC, lets us own the metric hooks, and
keeps TTL-aware eviction semantics under our control.

## 5. Components

### 5.1 `config`

Single `Config` struct parsed once at startup via viper. Immutable thereafter
(blocklist reload does not touch the struct). Precedence: **flags > env >
YAML > defaults**. Env vars use prefix `BNS_` and `__` for nesting
(`BNS_LISTEN__UDP=:5353`). Slice fields (`upstreams`) supported via repeated
`--upstream` flag and YAML list.

Validation runs after parse: bind addresses parse, at least one upstream,
at least one blocklist source (or empty-blocklist flag), all timeouts > 0.

### 5.2 `logging`

`slog.Logger` with JSON handler to stdout, level from config. Error output
to stderr. A separate `query` logger is created (always), but its handler
is a no-op when `query_log.enabled=false`.

Query log line shape:

```json
{"time":"...","level":"INFO","msg":"query","qname":"example.com.","qtype":"A",
 "client":"10.0.0.5:54321","outcome":"cache_hit","duration_ms":0.4,
 "upstream":""}
```

`outcome` is one of: `cache_hit`, `blocked`, `forwarded`, `error`.

### 5.3 `server`

Two pre-bound listeners (`net.PacketConn` for UDP, `net.Listener` for TCP)
handed to two `*dns.Server` instances. Both servers share a single
`Handler` implementation that adapts to the `Resolver` interface:

```go
type resolverHandler struct { r Resolver }

func (h *resolverHandler) ServeDNS(ctx context.Context, w dns.ResponseWriter, req *dns.Msg) {
    resp, err := h.r.Resolve(ctx, req)
    if err != nil || resp == nil {
        resp = servfail(req)
    }
    _ = w.WriteMsg(resp)
}
```

`*dns.Msg` is **pooled** by the server library — see §5.5 for ownership
rules in the cache.

A goroutine is spawned per server via `errgroup`. Shutdown is triggered by
SIGINT/SIGTERM: root context is cancelled, both `Server.Shutdown(ctx)` are
called with `shutdown_timeout`, then we wait for the errgroup.

### 5.4 `resolver`

```go
type Resolver interface {
    Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}
```

Each stage is a struct holding `next Resolver` and any stage-specific
dependencies. A stage either short-circuits with a response or delegates to
`next`. Chain order, outermost first:

```
metrics → query-log → blocklist → cache → coalesce → forward
```

Construction is procedural in `cmd/bns/main.go`:

```go
forward := forward.New(pool)
co      := coalesce.New(forward)
ch      := cache.New(co, lru)
bl      := blocklist.New(ch, matcherPtr)
ql      := qlog.New(bl, queryLogger)
m       := metrics.New(ql, registry)
handler := &resolverHandler{r: m}
```

A top-level `recover()` in the metrics stage converts panics into
`SERVFAIL` and increments `bns_panics_total`.

### 5.5 `cache`

In-tree LRU. Internal layout: doubly-linked list of `*entry` plus
`map[string]*list.Element`. Single `sync.Mutex` guards both. Key:

```
canonicalQname + "|" + qtypeStr + "|" + qclassStr
```

Qname canonicalised via `dnsutil.Canonical` (lowercase, trailing dot).

Entry:

```go
type entry struct {
    key       string
    response  *dns.Msg     // already deep-copied via dns.Copy at store time
    expiresAt time.Time
    storedAt  time.Time
    negative  bool
}
```

**Ownership rule (load-bearing):** `*dns.Msg` returned by the miekg server
or client may be returned to its internal pool after the handler returns.
Therefore:

- On **store**: deep-copy via `dns.Copy(resp)` before placing in the cache.
- On **hit**: deep-copy again via `dns.Copy(entry.response)` before
  returning, so two concurrent hits never alias the same `*Msg`.
- On **hit**: decrement each RR's `Header().TTL` by `time.Since(storedAt)`
  so downstream clients see the remaining TTL, not the original.

TTL computation at store time:

- Positive cache: TTL = `min(min-TTL-across-sections, cache.max_ttl)`,
  floored by `cache.min_ttl` if set.
- Negative cache: TTL = `min(SOA-MINIMUM-from-authority-section,
  cache.negative_ttl_max)`.
- SERVFAIL / network-failure damp: store synthetic negative entry with TTL
  `cache.serve_stale_on_failure_ttl` (default 5s) to prevent retry storms.

Capacity is bounded by entry count (`cache.capacity`). LRU eviction on
insert when full. Expired entries are evicted lazily on lookup.

### 5.6 `coalesce`

Coalesces concurrent identical in-flight queries into a single upstream
call. Today this is implemented over `golang.org/x/sync/singleflight`,
but the package name and metric names hide that detail so the
implementation can change (e.g. to a custom waitgroup map) without API
or dashboard churn.

Key is identical to the cache key. All piggyback callers receive
deep-copies of the shared response (pool ownership rules from §5.5
apply). Increments `bns_coalesced_queries_total` for each piggyback
caller (i.e. callers who did NOT initiate the upstream call).

### 5.7 `blocklist`

#### Source

```go
type Source interface {
    Load(ctx context.Context) ([]string, error)  // raw FQDNs, lowercased, trailing dot stripped
}
```

MVP impl: `FileSource{Path string}`. Reads one file per source. Multiple
sources are concatenated by the loader.

#### Parser

Accepts both `domains/` style and `hosts/` style hagezi output:

- Strip BOM if present.
- For each line:
  1. Trim leading/trailing whitespace.
  2. If line is empty, skip.
  3. If line starts with `#`, `;`, or `!`, skip (comment).
  4. Split on whitespace. If two fields and the first looks like an IPv4
     or IPv6 anchor (`0.0.0.0`, `127.0.0.1`, `::`, `::1`), take the
     second field; else take the first.
  5. Lowercase. Trim a trailing `.`.
  6. Validate: ASCII only (hagezi pre-converts IDN to punycode),
     LDH labels only, total length ≤ 253. Reject otherwise; increment
     parse-error counter; continue.
- Deduplicate via map during build.

Out of scope (MVP): AdBlock `||...^` syntax, dnsmasq `local=`,
RPZ zone files, allowlist / negation.

#### Matcher

Single `map[string]struct{}` keyed by lowercased FQDN (no trailing dot).
Lookup walks parent labels:

```
Match("foo.bar.example.com")
  → probe "foo.bar.example.com"
  → probe "bar.example.com"
  → probe "example.com"
  → probe "com"
  → first hit returns true; no hits returns false
```

`O(labels)` map lookups per query (labels ≤ ~8 in practice).

**Capacity target:** up to 1,000,000 entries (hagezi `pro.txt` is ~471K
today; headroom for combined or future lists). At ~32 B/string + Go map
overhead, ~50 MB is comfortably within Pi RAM.

**Atomic swap on reload:** the loader builds a fresh `*Matcher`, then
`atomic.Pointer[Matcher].Store(new)`. Readers do `Load()` once at the
start of the blocklist stage. Zero contention on hot path.

#### Reload

`SIGHUP` is the only trigger (hardcoded, not configurable). Handler in
`main` calls `blocklist.Reload(ctx)` which re-invokes every `Source.Load`,
re-parses, builds a new `*Matcher`, atomically swaps. Old `*Matcher`
becomes garbage. If reload fails (e.g. file missing), the previous
matcher stays in place; an error is logged and
`bns_blocklist_reloads_total{outcome="error"}` is incremented.

### 5.8 `upstream`

```go
type Upstream interface {
    Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
}
```

MVP impl: `UDPClient{Addr string, Timeout time.Duration, client *dns.Client}`.
Calls `client.Exchange(ctx, req, "udp", Addr)`. If `resp.Truncated == true`,
retries the same upstream once over `"tcp"`. (`miekg/dns` v2 does NOT
auto-retry on TC=1 — this is the caller's responsibility.) `*dns.Client`
is safe for concurrent use.

`Pool` holds an ordered slice of `Upstream` and implements primary +
fallback:

```go
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
    var last error
    for _, u := range p.upstreams {
        resp, err := u.Exchange(ctx, req)
        if err == nil { return resp, nil }
        last = err
        // log warn, record metric, continue to next
    }
    return nil, last
}
```

Each upstream call carries its own deadline (`upstream.timeout`); the
parent `query_timeout` is the overall budget.

### 5.9 `metrics`

Single `prometheus.Registry` created in `main`. Collectors registered on it
during component construction. `promhttp.HandlerFor(registry, ...)` mounted
on the admin server at `/metrics`.

Metric set (cardinality-conscious):

```
bns_queries_total{outcome, qtype}             counter
bns_query_duration_seconds{outcome}           histogram (default buckets)
bns_upstream_queries_total{upstream, outcome} counter
bns_upstream_duration_seconds{upstream}       histogram
bns_cache_entries                             gauge
bns_cache_capacity                            gauge
bns_cache_evictions_total                     counter
bns_blocklist_entries                         gauge
bns_blocklist_loaded_timestamp_seconds        gauge
bns_blocklist_reloads_total{outcome}          counter
bns_coalesced_queries_total                   counter
bns_panics_total                              counter
```

`outcome` ∈ `{hit, miss, blocked, error}`. `qtype` is the textual qtype
(`A`, `AAAA`, `CNAME`, `PTR`, `MX`, `TXT`, `NS`, `SOA`, `SRV`, `other`) —
cardinality capped at ~10. `upstream` cardinality = configured upstream
count (~2–3). No `qname` label.

Process and Go runtime metrics come from `promhttp` defaults.

### 5.10 `health`

- `GET /healthz` — always 200 once the process has started. Liveness only.
- `GET /readyz` — 200 only when all of:
  1. At least one blocklist has loaded successfully (or `blocklists.sources`
     is explicitly empty).
  2. Both DNS listeners are bound.
  3. One warmup query to the primary upstream has succeeded within
     `startup_probe_timeout`.

  Otherwise 503 with a JSON body listing which check failed.

### 5.11 `admin`

A single `*http.Server` bound to `admin.listen` (default `:9090`) hosting
`/metrics`, `/healthz`, `/readyz`. Started after the DNS listeners, stopped
before them on shutdown. Uses default `http.ServeMux`.

## 6. Data flow

End-to-end path for a single query:

1. `miekg` server reads a packet on UDP/TCP and spawns a goroutine.
2. Goroutine calls `resolverHandler.ServeDNS(ctx, w, req)`.
3. Handler invokes the resolver chain: `metrics → query-log → blocklist →
   cache → coalesce → forward`.
4. Each stage either short-circuits or delegates to `next`:
   - **metrics**: starts timer, defers observation, also installs panic
     recovery.
   - **query-log**: if enabled, logs `qname`, `qtype`, `client`. Outcome
     filled in on unwind.
   - **blocklist**: `matcher.Match(qname)`; on hit returns a synthesised
     NXDOMAIN (`resp.Response=true; resp.Rcode=dns.RcodeNameError;
     resp.Id=req.Id; resp.Question=req.Question`).
   - **cache**: lookup by `(qname|qtype|qclass)`. On non-expired hit,
     `dns.Copy(entry.response)`, decrement TTLs by age, return.
   - **coalesce**: deduplicates concurrent identical in-flight queries
     by `(qname|qtype|qclass)`; piggybacked callers get deep-copies of
     the originator's response.
   - **forward**: `pool.Exchange(ctx, req)`. On `TC=1`, retry that
     upstream once over TCP.
5. On unwind, the cache stage stores `dns.Copy(resp)` with computed
   `expiresAt`. Metrics records duration + outcome. Query-log emits the
   final outcome.
6. Handler writes the response with `w.WriteMsg(resp)`. Server returns the
   `*Msg` buffer to its pool.

### Edge cases

| Case                          | Behaviour                                          |
| ----------------------------- | -------------------------------------------------- |
| Empty `Question` section      | Respond `FORMERR`. Do not cache.                   |
| Multi-question (rare)         | Forward as-is. Do not cache. Do not blocklist-match. |
| Upstream all fail / timeout   | Respond `SERVFAIL`. Cache short-TTL negative.      |
| Upstream returns `NXDOMAIN`   | Respond as-is. Negative-cache with SOA-MIN TTL.    |
| `ctx` cancelled mid-flight    | Respond `SERVFAIL` best-effort. Do not cache.      |
| Panic anywhere in chain       | Respond `SERVFAIL`. Increment `bns_panics_total`.  |
| Truncated UDP response (TC=1) | Forwarder retries that upstream once over TCP.     |

## 7. Configuration schema

```yaml
listen:
  udp: ":53"            # bind for UDP listener
  tcp: ":53"            # bind for TCP listener
  query_timeout: 5s     # per-query overall deadline

upstreams:
  - addr: "1.1.1.1:53"
    timeout: 2s
  - addr: "9.9.9.9:53"
    timeout: 2s

cache:
  capacity: 10000              # max entries (LRU)
  min_ttl: 0s                  # 0 = honor upstream
  max_ttl: 86400s              # 24h cap
  negative_ttl_max: 900s       # 15m cap on negative-cache TTL
  serve_stale_on_failure_ttl: 5s

blocklists:
  sources:
    - type: file
      path: /var/lib/bns/hagezi-multi.txt
      format: auto             # auto | domains | hosts

admin:
  listen: ":9090"              # /metrics, /healthz, /readyz

logging:
  level: info                  # debug | info | warn | error
  format: json                 # json | text
  query_log:
    enabled: false

shutdown_timeout: 5s
startup_probe_timeout: 3s
```

CLI flag and env var equivalents are derived automatically by viper.
Examples:

```
bns serve --listen.udp=:5353 --upstream=1.1.1.1:53 --upstream=9.9.9.9:53
BNS_LISTEN__UDP=:5353 BNS_LOGGING__LEVEL=debug bns serve
```

Reload of blocklists is via `SIGHUP` to the running process. The reload
mechanism is hardcoded and is not exposed in config.

A complete example config and a small sample blocklist will be shipped
under `examples/`.

## 8. Errors and observability

See §5.9 for metrics. Logging policy:

| Event                                     | Level   |
| ----------------------------------------- | ------- |
| Startup config summary                    | info    |
| Listener bound                            | info    |
| Blocklist loaded (entries, duration)      | info    |
| Blocklist parse error per-line (summary)  | warn    |
| Upstream failover                         | warn    |
| All upstreams failed for query            | warn    |
| Panic recovered                           | error   |
| Shutdown started / completed              | info    |
| Per-query (when query log enabled)        | info    |

## 9. Testing strategy

Per CLAUDE.md: TDD red-green-refactor. Standard `testing` package plus
`testify/require`.

| Layer                                            | Type         | Coverage focus                                                                 |
| ------------------------------------------------ | ------------ | ------------------------------------------------------------------------------ |
| `blocklist.Matcher`                              | unit (table) | exact, suffix, miss, case-insensitive, trailing-dot, IDN-as-punycode           |
| `blocklist` parser                               | unit         | domains, hosts-style, comments (`#`/`;`/`!`), blanks, malformed counted only   |
| `blocklist.FileSource`                           | unit         | tmpfile, missing file → error, ctx cancel                                       |
| `blocklist` reload                               | unit         | atomic swap, failure preserves previous matcher                                |
| `cache`                                          | unit         | TTL expiry, eviction order, capacity, deep-copy ownership, TTL decrement on hit |
| `upstream.UDPClient`                             | unit (fake `*dns.Client`-ish via in-process miekg server) | success, timeout, TC=1 → TCP retry |
| `upstream.Pool`                                  | unit         | primary success, fallback on error, all-fail, ctx cancel                       |
| Each resolver stage                              | unit         | with fake `next`, asserts pass-through vs short-circuit semantics              |
| Resolver chain integration                       | integration  | in-process fake upstream → drive via `dns.Client`, assert all 8 success criteria |
| `server` listeners                               | integration  | bind `127.0.0.1:0`, exchange via `dns.Client`, assert response                  |
| `config`                                         | unit         | precedence (flags > env > YAML > defaults), validation errors                   |

`-race` enabled in CI. No mocking framework; fakes are hand-written and
live next to the package they fake out.

## 10. Out of scope / future extensions

Designed to slot in without churn (interfaces already present):

- **DoH / DoT / DoQ upstream protocols** — add new `Upstream` impls.
- **URL-based blocklist sources with periodic refresh** — add `URLSource`
  implementing `blocklist.Source`. Refresh ticker triggers the existing
  reload pipeline.
- **DNSSEC validation** — add a `validator` resolver stage.
- **Per-client policy / rewrites** — add resolver stages.
- **Allowlist** — extend `Matcher` with a second negative map probed
  before the block decision.

Out of MVP and not explicitly designed-for:

- Web UI / admin API.
- Authoritative serving / zone transfers.
- ECS (EDNS Client Subnet) handling beyond pass-through.
- Persistence across restarts.
- Distribution as systemd unit, Dockerfile, or OS package.

## 11. MVP success criteria (from seed)

These are the acceptance gates for declaring MVP done. The integration
test suite must cover each.

1. `bns serve` runs locally without error.
2. `dig @127.0.0.1 example.com` returns the upstream's answer.
3. With `query_log.enabled: true`, each query produces a log line.
4. `curl 127.0.0.1:9090/metrics` returns Prometheus exposition.
5. Identical configuration via flags, env, and YAML produces identical
   behaviour.
6. `curl 127.0.0.1:9090/healthz` and `/readyz` behave per §5.10.
7. `dig @127.0.0.1 <domain-in-blocklist>` returns NXDOMAIN.
8. A second `dig` for the same uncached domain returns measurably faster
   than the first (cache hit observable via metrics and logs).

## 12. References

- Seed prompt: `docs/prompts/seed.md`
- hagezi/dns-blocklists: <https://github.com/hagezi/dns-blocklists>
- miekg/dns v2: <https://codeberg.org/miekg/dns>
- RFC 2308 (negative caching)
- RFC 1035 (DNS basics)
