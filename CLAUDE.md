# CLAUDE.md

Guide Claude Code (claude.ai/code) work code this repo.

## Project

BNS (Ben's Name Server) — caching DNS forwarder with ad-block for small private network. Pi-hole-like. Go 1.26, module `github.com/bcrisp4/bns`, single static binary deploy on Raspberry Pi.

MVP shipped to `main`. Releases: v0.1.0 (2026-05-19, MVP), v0.2.0 (container image multi-arch amd64+arm64), v0.3.0 (HTTP blocklist source, polite auto-refresh, on-disk cache, fail-open), v0.4.0 (2026-05-24, DoH upstream with operator-pinned endpoint_ips, RFC 8484 compliant, per-query upstream attribution in qlog). Container at `ghcr.io/bcrisp4/bns`. Tests race-clean. Specs in `docs/specs/`, plans in `docs/plans/`.

## Quickstart

```bash
make build
# Bind off :53 (mDNS owns UDP/5353 on most Linux; avoid that too)
BNS_LOGGING__QUERY_LOG__ENABLED=true BNS_LOGGING__LEVEL=debug \
  ./bin/bns serve -c examples/config.example.yaml \
  --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354
```

Smoke test other terminal:

```bash
dig @127.0.0.1 -p 5354 example.com         # forwarded
dig @127.0.0.1 -p 5354 ads.example         # NXDOMAIN (sample blocklist)
curl http://127.0.0.1:9090/metrics | grep bns_
```

Reload blocklists in place: `pkill -HUP -f bin/bns`.

## Tech stack — non-obvious

- **DNS library: `codeberg.org/miekg/dns` v2** (cheat sheet below). NOT `github.com/miekg/dns` (v1, unmaintained). Fetch docs via Context7 / codeberg, not stale v1.
- **DoH upstream:** `internal/upstream/doh_client.go` — hand-rolled over stdlib `net/http`,
  uses miekg/dns v2 `dnshttp.Response` for decode, bypasses `dnshttp.NewRequest`
  to avoid hardcoded `/dns-query` path append (footgun for natural config URLs).
  Operator-pinned `endpoint_ips` substitute hostname in `Transport.DialContext`;
  hostname retained for SNI / cert SAN match. HTTP/2 preferred, HTTP/1.1 fallback,
  TLS 1.3 floor.
- Metrics: Prometheus (`/metrics`).
- Logging: stdlib `slog`, structured, stdout/stderr.
- CLI: `cobra`. Config: `viper` (flags + env + YAML, that precedence).
- Health: `/healthz` (liveness), `/readyz` (readiness).

## Architecture

`cmd/bns/main.go` parse flags, build signal-cancelled ctx, run `serve.go:runServe`. `serve.go` wire every component, run UDP+TCP listeners + admin HTTP + SIGHUP reload + shutdown under one `errgroup`.

### Package layout

```
cmd/bns/                          main + cobra serve subcommand + signal handling
internal/config/                  viper schema + Load + Validate
internal/logging/                 slog factories + QueryLog gate
internal/blocklist/               Parse, FileSource, HTTPSource (disk-only), CacheStore (atomic write+sweep),
                                  Fetcher (HTTP client + ticker + RefreshNow + orphan sweep),
                                  BootstrapResolver (custom net.Resolver via upstream IPs),
                                  Matcher (parent-walk), Holder (atomic swap), Loader
internal/cache/                   in-tree LRU + CloneMsg helper
internal/upstream/                Upstream interface (Name+Protocol getters), UDPClient, DoHClient,
                                  Pool (sequential failover), factory.New(cfg)→Upstream,
                                  testutil/{Spawn, NewTLSCert}
internal/resolver/                Resolver interface, Handler adapter (writes SERVFAIL on err),
                                  Outcome classifier (ctx-marker for block vs upstream NXDOMAIN)
internal/resolver/chain/          Build() composes the chain in canonical order
internal/resolver/{block,cache,coalesce,forward,metric,qlog}stage/  one package per stage
internal/metrics/                 Prometheus collectors + CacheObserver adapter
internal/health/                  Readiness aggregator + /healthz + /readyz handlers
internal/admin/                   Mux mounting /metrics + /healthz + /readyz
internal/server/                  UDP+TCP dns.Server pair with NotifyStartedFunc Ready sync
internal/integration/             End-to-end MVP test
```

### Resolver chain (composed outside-in)

```
metrics → qlog → block → cache → coalesce → forward
```

- `metrics` (outermost): time every query, record `bns_queries_total{outcome,qtype}` + duration histogram, recover panics into SERVFAIL + `bns_panics_total++`. Install block marker into ctx.
- `qlog`: emit one JSON line per query (qname, qtype, outcome, duration_ms; plus `client` host:port and `proto` udp/tcp when handler installs `ClientInfo` in ctx) — no-op when `query_log.enabled=false`.
- `block`: `Holder.Current().Match(qname)` — on hit, call `resolver.MarkBlocked(ctx)` and synth NXDOMAIN (set Response=true, Rcode=NameError, copy ID + Question).
- `cache`: lookup by `cache.Key(q)`; hit return deep-copy with TTLs decremented by age; miss call next, compute per-RR min TTL (capped by `cfg.Cache.MaxTTL`, negative-cache use SOA Minttl capped by `NegativeTTLMax`), store deep-copy.
- `coalesce`: `singleflight.Group.Do` keyed by `cache.Key`; piggyback callers get `cache.CloneMsg` of originator response; `shared==true` increments `bns_coalesced_queries_total`.
- `forward` (terminal): hand req to configured `upstream.Upstream` (Pool).

### Key interfaces (extensibility seams)

| Interface              | Where                       | Contract                                                           | Future impls                       |
| ---------------------- | --------------------------- | ------------------------------------------------------------------ | ---------------------------------- |
| `resolver.Resolver`    | `internal/resolver`         | `Resolve(ctx, req) (*dns.Msg, error)` — never mutate req, never return both | DNSSEC validator, ECS, rewrites |
| `upstream.Upstream`    | `internal/upstream`         | `Exchange(ctx, req) (*dns.Msg, error)` + `Name() string` + `Protocol() string` | DoT, DoH3           |
| `blocklist.Source`     | `internal/blocklist`        | `Load(ctx) ([]string, error)` — return canonical lowercased FQDNs (disk-only; no network) | FTP / S3 / git sources           |
| `logging.QueryLog`     | `internal/logging`          | `LogQuery(attrs ...slog.Attr)` — no-op when disabled                | (stable)                          |
| `cache.MetricsObserver`| `internal/cache`            | `SetEntries(int)`, `IncEvictions()` — wired via `metrics.CacheObserver()` | (stable)                    |

### Architecture invariants

Preserve when design:

- **Two listeners** (UDP + TCP) share one `dns.Handler`. Forwarder protocol pluggable via `Upstream` interface.
- **Blocklist match** exact + subdomain wildcard. `ads.com` blocks `ads.com` and `*.ads.com`. Capacity target up to 1M entries (hagezi `pro.txt` ~471K today).
- **Cache** in-memory, bounded, TTL-aware, lazy expiry on Get, LRU eviction on Store-when-full. Negative cache use SOA MIN capped by `NegativeTTLMax`. Not persistent.
- **Ownership of `*dns.Msg`**: cache and coalesce both deep-copy on store AND on get. miekg server pools `*Msg` — aliasing cached value with live request corrupts both. Use `cache.CloneMsg`, NOT `m.Copy()` (shallow on RR slices).
- **Upstream dedup** via `singleflight` (impl detail of `coalesce` — don't reference singleflight from outside).
- **Blocklist atomic swap**: `Holder` wraps `atomic.Pointer[Matcher]`. Readers do lock-free `Current().Match(qname)`. SIGHUP reload calls `Loader.Load`, builds fresh `*Matcher`, `Holder.Swap(next)`.
- **Server start/shutdown race**: `dns.Server.init` writes internal state inside `ListenAndServe`. Wait on `Server.Ready(ctx)` before declaring listeners up or invoking Shutdown.
- **Resolver Resolve contract**: return `(resp, nil)` or `(nil, err)`, never `(resp, err)`. Impls must not mutate req. Block stage and Handler synth responses with `Response=true`, `ID=req.ID`, `Question=req.Question`.
- **Outcome label** carried via ctx-value (`resolver.MarkBlocked(ctx)` from blockstage; `resolver.Outcome(ctx, resp, err)` from metric + qlog stages) — keep "blocked" (synthetic) distinct from "nxdomain" (upstream).
- **Thread-safety everywhere** — listeners, cache, blocklist, metrics all hit concurrently. Run `make race` before declaring done.
- **Query log off by default**; gated behind `logging.query_log.enabled`.

## Performance posture

Target: few thousand QPS on Raspberry Pi-class hardware. NOT high-scale.

- Allocation-conscious on hot path (query handle → cache lookup → forward → respond). Reuse buffers where library allows.
- Mind GC pauses — large blocklist structures allocated once at load, not churned.
- **No pre-optimize.** YAGNI/KISS. Profile with `pprof` first; optimize only when measurement justifies.

## Development practices (enforced by user)

- **TDD, red-green-refactor.** Write failing test first.
- Idiomatic Go. Boring, well-trodden patterns over clever ones.
- Document public *and* internal interfaces. Extra care on load-bearing or non-obvious logic — explain *why*.
- Designs/specs belong in `docs/specs`, committed.
- Impl plans belong in `docs/plans`, committed.
- Build binaries to `bin/` (gitignored). Example: `go build -o bin/bns ./cmd/bns`.

## miekg/dns v2 — API cheat sheet

Import path `codeberg.org/miekg/dns` (NO `/v2` suffix despite v2). Field/function diffs vs v1 that bit us:

- `Msg.ID` (uppercase), not `Id`. Note: `ID`/`Response`/`Rcode` live on embedded `MsgHeader`, NOT directly on `Msg` — `dns.Msg{Response: true}` fails compile. Use `new(dns.Msg); m.Response = true` or `dns.Msg{MsgHeader: dns.MsgHeader{Response: true}}`.
- Build request via `dns.NewMsg(name, qtype)`. No `req.SetQuestion(...)`.
- Wire reply via `dnsutil.SetReply(resp, req)` from `codeberg.org/miekg/dns/dnsutil`. No `resp.SetReply(req)`.
- Parse RR text via `dns.New(s)`, not `dns.NewRR(s)`.
- `Msg.Question` is `[]dns.RR` (not `[]dns.Question`). Extract qname via `q.Header().Name`.
- Header has no `Rrtype`. Get qtype string via `dns.TypeToString[dns.RRToType(rr)]`.
- `Msg.Rcode` is uint16; cast constants when compare: `uint16(dns.RcodeNameError)`.
- `Client`: `dns.NewClient()` + `c.Transport.ReadTimeout = ...`; network is arg to `Exchange(ctx, m, network, addr)`.
- `Handler.ServeDNS(ctx, w, r)` — ctx-first.
- `Server.Shutdown(ctx)` returns void. Server races with init() — wait on `NotifyStartedFunc` (see `internal/server/server.go`).
- `ResponseWriter` has NO `WriteMsg`; use `m.WriteTo(w)` or `m.Pack()` + `io.Copy(w, m)`.
- `Msg.Copy()` shallow on RR slices. For pool-safe deep copies use `cache.CloneMsg` (clones each RR via `rr.Clone()`).
- TC=1 truncation does NOT auto-retry — caller dials TCP explicit (see `internal/upstream/udp_client.go`).
- **RR construction shape changed.** Header is `dns.Header` (not `dns.RR_Header`), TTL uppercase (no `Ttl`), no `Rrtype` on Header. `dns.A`/`AAAA`/`NS`/`SOA` embed `rdata.X` sub-struct: `dns.A{Hdr: dns.Header{...}, A: rdata.A{Addr: netip.MustParseAddr("1.2.3.4")}}`. Field accessors work via promotion (`a.Hdr.TTL`, `ns.Ns`) but construction needs sub-struct.
- **Reliable `Msg.Pack()` error trigger for tests:** Question with 64-char label (label length >= 64 violates RFC 1035 §2.3.4, hits miekg's pack.Name `labelLen >= 1<<6` guard). Used for `defer`-restore-on-Pack-fail tests in `internal/upstream/doh_client_test.go`.

Vendored offline reference: `/home/ben.guest/vendor/miekg-dns-v2/`.

## Local vendored references

- `/home/ben.guest/vendor/miekg-dns-v2/` — codeberg miekg/dns v2 source. Read for API confirm before guess.
- `/home/ben.guest/vendor/hagezi-dns-blocklists/` — hagezi lists. Canonical format `domains/` (newline-delimited FQDNs, `#` header comments only, punycode IDNs). Files big — never `Read` whole thing; use `head`/`wc -l`/`grep`.

## Container

- Image: `ghcr.io/bcrisp4/bns` — multi-arch (`linux/amd64`+`linux/arm64`), `gcr.io/distroless/static-debian12:nonroot`. ~10MB.
- Build: `deploy/docker/Dockerfile` (2 stages — Go cross-compile via `GOOS/GOARCH` from `TARGETOS/TARGETARCH`, distroless runtime). Image size ~25 MB. Hagezi list no longer baked; fetched at runtime by in-process Fetcher, persisted to `/var/cache/bns/blocklists` (declared as `VOLUME`).
- CI: `.github/workflows/docker.yml` — buildx multi-arch + GHCR push on `main` + `v*` tags.
- **Git tag → image tag transform.** `docker/metadata-action` uses `pattern={{version}}`, so git tag `v0.2.0` publishes `ghcr.io/bcrisp4/bns:0.2.0` (no `v`), plus `:0.2` and `:sha-XXXXXXX`. Main pushes also publish `:latest`.
- **Container listens `:5354`** — nonroot uid 65532 cannot bind privileged ports; distroless has no `setcap` so `CAP_NET_BIND_SERVICE` route unavailable. Host maps `-p 53:5354/udp -p 53:5354/tcp`.
- Default config (`deploy/docker/config.yaml`) configures Fetcher with one HTTP source pointing at hagezi `main` branch `pro.txt`. Cold start with no cache volume = serves without blocklist for ~seconds until first fetch lands; mount named volume at `/var/cache/bns` to preserve cache across restarts (`docker volume create bns-cache; docker run -v bns-cache:/var/cache/bns ...`).
- Config baked at `/etc/bns/config.yaml` (source: `deploy/docker/config.yaml`). Override at runtime via bind-mount, `BNS_*` env vars, or trailing CLI flags after image name.
- Reload blocklist without restart: `docker kill -s HUP <container>`.

## Build and test

- `make build` — `bin/bns` static binary
- `make test` — go test ./...
- `make race` — race detector (preferred — concurrency load-bearing)
- `make vet` — go vet
- `make lint` — golangci-lint (NOT installed locally; CI only)
- `make tidy` — go mod tidy
- `make build-stress` — `bin/bns-stress` orchestrator (see Stress harness)

Test helpers: `metrics.NewForTest()` returns `*Metrics` registered to fresh `prometheus.NewRegistry()` — use in any unit test that constructs `*Metrics` without polluting default registerer. `internal/upstream/testutil.NewTLSCert(t, hosts)` generates self-signed cert with mixed DNS / IP SANs (split via `net.ParseIP`); pair with `httptest.NewUnstartedServer` + `srv.TLS.Certificates` + `StartTLS()` for any DoH/h2 test.

## Gotchas

- **Port 5353 is mDNS.** avahi-daemon / systemd-resolved listens on UDP/5353; bind there gives `address already in use` on UDP only (TCP bind succeeds, masks cause). Use 5354 or other free port.
- **`serve` cobra flags minimal** — `--config/-c`, `--listen.udp`, `--listen.tcp`, `--pprof`. `--blocklist` removed when HTTP source landed; `--upstream` removed when DoH landed; use YAML for both. Everything else (log level, query log, cache capacity, blocklist sources, upstreams) via env (`BNS_*`, `__` for nesting) or YAML. Env vars cannot index slice config (see viper gotcha).
- **viper `AutomaticEnv` does NOT index slices via env vars.** `BNS_BLOCKLISTS__SOURCES__0__PATH` ignored — viper never enumerates env keys; only checks env on `Get("key.path")` calls, slice unmarshal doesn't iterate indexed env. Slice config (blocklists.sources, upstreams) must come from YAML (`-c file.yaml`). For HTTP blocklists, `blocklists.refresh_interval` and `blocklists.cache_dir` are scalars and ARE settable via env (`BNS_BLOCKLISTS__REFRESH_INTERVAL=12h`).
- **Source `name` required.** Every entry under `blocklists.sources` must have `name:` (string, unique). It's `{source="..."}` label on every `bns_blocklist_*` metric. Validation rejects missing or duplicate names with fail-fast error at startup.
- **HTTPSource.Load disk-only.** Network I/O lives entirely in `Fetcher` (separate goroutine). HTTPSource just reads cached body. Keeps request-serving path off network.
- **Bootstrap dialer must NOT route through BNS resolver chain.** Dials configured upstream IPs directly via custom `net.Resolver`. Routing fetcher DNS through chain = deadlock (chain busy serving the very request fetcher tries to bootstrap).
- **Cache orphan sweep runs once at startup only.** Removing source from config without restart leaves cache file on disk until next restart. Documented; not bug.
- **Manual smoke leaves bns on `:9090`.** Background-launched bns from manual test not killed when shell command ends. Check `ss -ltnp | grep :9090` and kill before re-running another bns.
- **Outcome label divergence from spec**: `bns_queries_total{outcome}` ∈ `{blocked, nxdomain, forwarded, error}` — NOT spec's `{hit, miss, blocked, error}`. Cache hit/miss not propagated through the outer metric stage; use the dedicated `bns_cache_lookups_total{result="hit"|"miss"}` counter (emitted directly by cachestage) for hit-rate. Hit rate = `rate(bns_cache_lookups_total{result="hit"}[5m]) / rate(bns_cache_lookups_total[5m])`.
- **GHCR package visibility inherits from repo on first push.** Public repo → public package automatically; no manual flip. Local `gh` token here lacks `read:packages`/`write:packages` scopes — query via anonymous `docker pull` (after `docker logout ghcr.io`), not GHCR REST API.
- **Dev host is arm64.** `docker buildx build` default platform = arm64; pass `--platform linux/amd64` explicitly for cross-arch smoke. Multi-arch `buildx --load` not supported (single-arch only) — verify multi-arch via CI or push to registry.
- **Discard logger idiom** — use `slog.New(slog.DiscardHandler)` (Go 1.24+), not `slog.New(slog.NewTextHandler(io.Discard, nil))`. Latter still allocates and calls `Enabled` before dropping.
- **Don't set `Accept-Encoding: gzip` on outgoing requests** unless also wrap response with `gzip.NewReader`. Stdlib `net/http` Transport only does transparent decompression when caller does NOT set header explicitly. Setting once silently disables decode and hands raw gzip bytes back.
- **Run `find . -name '*.go' -not -path '*/vendor/*' | xargs gofmt -l` before commit** — `make lint` is CI-only, gofmt local. Catches whitespace/alignment drift cheaply.
- **DoH upstreams require operator-pinned `endpoint_ips`.** BNS never resolves DoH URL hostname at runtime — would deadlock through itself on Pi-hole-style deployments. Hostname used for SNI + cert SAN only.
- **Blocklist fetcher reuses DoH `endpoint_ips` as bootstrap DNS targets paired with `:53`.** Assumes same IPs serving DoH on `:443` also serve plain UDP/TCP DNS on `:53` — true for major public providers, breaks for self-hosted DoH-only (see `docs/TODO.md`).
- **httptest TLS h2 needs `NextProtos: []string{"h2","http/1.1"}`** on `srv.TLS` before `StartTLS()`. Without, ALPN offers nothing, client silently falls back to HTTP/1.1 — h2 tests (multiplexing, push refusal) become no-ops.

## Stress harness

`cmd/bns-stress` orchestrates `mockupstream` + `bns serve` + dnspyre (imported as library). One run produces `dist/stress/<RFC3339>/{report.md,*.pprof,*.prom}`.

```
make build-stress
./bin/bns-stress --scenario mixed --duration 60s --concurrency 50 \
  --blocklist /home/ben.guest/vendor/hagezi-dns-blocklists/domains/pro.txt \
  --max-io-errors 200
```

Spec: `docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md`. Note: dnspyre `@<path>` query-list syntax is kingpin CLI sugar — library callers (orchestrator) must expand themselves. `--max-io-errors` absorbs dnspyre tail-cancellation artefact in short runs.