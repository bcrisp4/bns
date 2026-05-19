# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

BNS (Ben's Name Server) — caching DNS forwarder with ad-blocking for a small private network. Pi-hole-like. Go, single-binary, deployable on a Raspberry Pi.

Module: `github.com/bcrisp4/bns`. Go 1.26.

## Status

MVP shipped on `feat/mvp` (47 commits, not yet merged to `main`). All 17 internal packages exist; full chain works end-to-end; tests race-clean. See `docs/specs/2026-05-19-bns-mvp-design.md` and `docs/plans/2026-05-19-bns-mvp-implementation.md`.

## Tech stack — non-obvious

- **DNS library: `codeberg.org/miekg/dns` v2.** NOT `github.com/miekg/dns` (v1, unmaintained). The v2 API is incompatible with v1. Do not import the GitHub path. When fetching docs, use Context7 / codeberg, not the stale GH v1 docs.
- Metrics: Prometheus (`/metrics`).
- Logging: stdlib `slog`, structured, stdout/stderr.
- CLI: `cobra`. Config: `viper` (flags + env + YAML, in that precedence).
- Health: `/healthz` (liveness), `/readyz` (readiness).

## Architecture

`cmd/bns/main.go` parses flags, builds a signal-cancelled ctx, runs `serve.go:runServe`. `serve.go` wires every component and runs UDP+TCP listeners + admin HTTP + SIGHUP reload + shutdown under one `errgroup`.

### Package layout

```
cmd/bns/                          main + cobra serve subcommand + signal handling
internal/config/                  viper schema + Load + Validate
internal/logging/                 slog factories + QueryLog gate
internal/blocklist/               Parse, FileSource, Matcher (parent-walk), Holder (atomic swap), Loader
internal/cache/                   in-tree LRU + CloneMsg helper
internal/upstream/                Upstream interface, UDPClient (TC=1 retry), Pool (primary+fallback)
internal/resolver/                Resolver interface, Handler adapter (writes SERVFAIL on err),
                                  Outcome classifier (ctx-marker for block vs upstream NXDOMAIN)
internal/resolver/chain/          Build() composes the chain in canonical order
internal/resolver/{block,cache,coalesce,forward,metric,qlog}stage/  one package per stage
internal/metrics/                 Prometheus collectors + CacheObserver adapter
internal/health/                  Readiness aggregator + /healthz + /readyz handlers
internal/admin/                   Mux mounting /metrics + /healthz + /readyz
internal/server/                  UDP+TCP dns.Server pair with NotifyStartedFunc Ready sync
internal/upstream/testutil/       Spawn helper booting in-process dns.Server for tests
internal/integration/             End-to-end MVP test
```

### Resolver chain (composed outside-in)

```
metrics → qlog → block → cache → coalesce → forward
```

- `metrics` (outermost): times every query, records `bns_queries_total{outcome,qtype}` + duration histogram, recovers panics into SERVFAIL + `bns_panics_total++`. Installs the block marker into ctx.
- `qlog`: emits one JSON line per query (qname, qtype, outcome, duration_ms) — no-op when `query_log.enabled=false`.
- `block`: `Holder.Current().Match(qname)` — on hit, calls `resolver.MarkBlocked(ctx)` and synthesises NXDOMAIN (sets Response=true, Rcode=NameError, copies ID + Question).
- `cache`: lookup by `cache.Key(q)`; hit returns deep-copy with TTLs decremented by age; miss calls next, computes per-RR min TTL (capped by `cfg.Cache.MaxTTL`, negative-cache uses SOA Minttl capped by `NegativeTTLMax`), stores deep-copy.
- `coalesce`: `singleflight.Group.Do` keyed by `cache.Key`; piggybacked callers get `cache.CloneMsg` of the originator's response; `shared==true` increments `bns_coalesced_queries_total`.
- `forward` (terminal): hands req to the configured `upstream.Upstream` (the Pool).

### Key interfaces (extensibility seams)

| Interface              | Where                       | Contract                                                           | Future impls                       |
| ---------------------- | --------------------------- | ------------------------------------------------------------------ | ---------------------------------- |
| `resolver.Resolver`    | `internal/resolver`         | `Resolve(ctx, req) (*dns.Msg, error)` — never mutate req, never return both | DNSSEC validator, ECS, rewrites |
| `upstream.Upstream`    | `internal/upstream`         | `Exchange(ctx, req) (*dns.Msg, error)`                              | DoH / DoT / DoQ clients           |
| `blocklist.Source`     | `internal/blocklist`        | `Load(ctx) ([]string, error)` — return canonical lowercased FQDNs   | URL fetcher with refresh ticker   |
| `logging.QueryLog`     | `internal/logging`          | `LogQuery(attrs ...slog.Attr)` — no-op when disabled                | (stable)                          |
| `cache.MetricsObserver`| `internal/cache`            | `SetEntries(int)`, `IncEvictions()` — wired via `metrics.CacheObserver()` | (stable)                    |

### Architecture invariants

Preserve when designing:

- **Two listeners** (UDP + TCP) share one `dns.Handler`. Forwarder protocol pluggable via `Upstream` interface.
- **Blocklist matching** is exact + subdomain wildcard. `ads.com` blocks `ads.com` and `*.ads.com`. Capacity target up to 1M entries (hagezi `pro.txt` ~471K today).
- **Cache** is in-memory, bounded, TTL-aware, lazy expiry on Get, LRU eviction on Store-when-full. Negative caching uses SOA MIN capped by `NegativeTTLMax`. Not persistent.
- **Ownership of `*dns.Msg`**: cache and coalesce both deep-copy on store AND on get. The miekg server pools `*Msg` — aliasing a cached value with a live request corrupts both. Use `cache.CloneMsg`, NOT `m.Copy()` (shallow on RR slices).
- **Upstream dedup** via `singleflight` (impl detail of `coalesce` package — don't reference singleflight from outside).
- **Blocklist atomic swap**: `Holder` wraps `atomic.Pointer[Matcher]`. Readers do lock-free `Current().Match(qname)`. SIGHUP reload calls `Loader.Load`, builds a fresh `*Matcher`, `Holder.Swap(next)`.
- **Server start/shutdown race**: `dns.Server.init` writes internal state inside `ListenAndServe`. Wait on `Server.Ready(ctx)` before declaring listeners up or invoking Shutdown.
- **Resolver Resolve contract**: return `(resp, nil)` or `(nil, err)`, never `(resp, err)`. Implementations must not mutate req. Block stage and Handler synthesise responses with `Response=true`, `ID=req.ID`, `Question=req.Question`.
- **Outcome label** carried via ctx-value (`resolver.MarkBlocked(ctx)` from blockstage; `resolver.Outcome(ctx, resp, err)` from metric + qlog stages) — keeps "blocked" (synthetic) distinct from "nxdomain" (upstream).
- **Thread-safety required everywhere** — listeners, cache, blocklist, metrics all hit concurrently. Run `make race` before declaring done.
- **Query logging off by default**; gated behind `logging.query_log.enabled`.

## Performance posture

Target: a few thousand QPS on Raspberry Pi-class hardware. NOT high-scale.

- Be allocation-conscious on the hot path (query handle → cache lookup → forward → respond). Reuse buffers where the library allows.
- Mind GC pauses — large blocklist structures should be allocated once at load, not churned.
- **Do not pre-optimize.** YAGNI/KISS. Profile with `pprof` first; only optimize when a measurement justifies it.

## Development practices (enforced by user)

- **TDD, red-green-refactor.** Write the failing test first.
- Idiomatic Go. Boring, well-trodden patterns over clever ones.
- Document public *and* internal interfaces. Extra care on load-bearing or non-obvious logic — explain *why*.
- Designs/specs belong in `docs/specs` and get committed.
- Implementation plans belong in `docs/plans` and get committed.
- Build binaries to `bin/` (gitignored). Example: `go build -o bin/bns ./cmd/bns`.

## miekg/dns v2 — API cheat sheet

Import path is `codeberg.org/miekg/dns` (NO `/v2` suffix despite being v2). Field/function differences vs v1 that bit us:

- `Msg.ID` (uppercase), not `Id`.
- Build a request via `dns.NewMsg(name, qtype)`. No `req.SetQuestion(...)`.
- Wire a reply via `dnsutil.SetReply(resp, req)` from `codeberg.org/miekg/dns/dnsutil`. No `resp.SetReply(req)`.
- Parse RR text via `dns.New(s)`, not `dns.NewRR(s)`.
- `Msg.Question` is `[]dns.RR` (not `[]dns.Question`). Extract qname via `q.Header().Name`.
- Header has no `Rrtype`. Get qtype string via `dns.TypeToString[dns.RRToType(rr)]`.
- `Msg.Rcode` is uint16; cast constants when comparing: `uint16(dns.RcodeNameError)`.
- `Client`: `dns.NewClient()` + `c.Transport.ReadTimeout = ...`; network is an arg to `Exchange(ctx, m, network, addr)`.
- `Handler.ServeDNS(ctx, w, r)` — ctx-first.
- `Server.Shutdown(ctx)` returns void. Server races with init() — wait on `NotifyStartedFunc` (see `internal/server/server.go`).
- `ResponseWriter` has NO `WriteMsg`; use `m.WriteTo(w)` or `m.Pack()` + `io.Copy(w, m)`.
- `Msg.Copy()` is shallow on RR slices. For pool-safe deep copies use `cache.CloneMsg` which clones each RR via `rr.Clone()`.
- TC=1 truncation does NOT auto-retry — caller dials TCP explicitly (see `internal/upstream/udp_client.go`).

Vendored offline reference: `/home/ben.guest/vendor/miekg-dns-v2/`.

## Local vendored references

- `/home/ben.guest/vendor/miekg-dns-v2/` — codeberg miekg/dns v2 source. Read for API confirmation before guessing.
- `/home/ben.guest/vendor/hagezi-dns-blocklists/` — hagezi lists. Canonical format is `domains/` (newline-delimited FQDNs, `#` header comments only, punycode IDNs). Files are large — never `Read` the whole thing; use `head`/`wc -l`/`grep`.

## Build and test

- `make build` — `bin/bns` static binary
- `make test` — go test ./...
- `make race` — race detector (preferred — concurrency is load-bearing)
- `make vet` — go vet
- `make lint` — golangci-lint (NOT installed locally; CI only)
- `make tidy` — go mod tidy

## Known MVP divergences from spec

- Metric `bns_queries_total` outcome label set is `{blocked, nxdomain, forwarded, error}` — NOT spec's `{hit, miss, blocked, error}`. Cache hit/miss is not propagated to the outer metric stage; derive cache hit rate from `bns_upstream_queries_total` vs `bns_queries_total`.
- `serve` cobra subcommand only exposes flags `--config`, `--listen.udp`, `--listen.tcp`, `--upstream`. Everything else (log level, query log, cache capacity, etc.) configured via env (`BNS_*`, `__` for nesting) or YAML.
