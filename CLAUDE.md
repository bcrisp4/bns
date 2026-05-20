# CLAUDE.md

File give guide to Claude Code (claude.ai/code) when work code in this repo.

## Project

BNS (Ben's Name Server) — caching DNS forwarder with ad-block for small private network. Pi-hole-like. Go 1.26, module `github.com/bcrisp4/bns`, single static binary deploy on Raspberry Pi.

MVP shipped to `main`. v0.1.0 release tagged 2026-05-19. Container image at `ghcr.io/bcrisp4/bns` (multi-arch amd64+arm64). 17 internal packages, full chain work end-to-end, tests race-clean. See `docs/specs/2026-05-19-bns-mvp-design.md` and `docs/plans/2026-05-19-bns-mvp-implementation.md`.

## Quickstart

```bash
make build
# Bind off :53 (mDNS owns UDP/5353 on most Linux; avoid that too)
BNS_LOGGING__QUERY_LOG__ENABLED=true BNS_LOGGING__LEVEL=debug \
  ./bin/bns serve -c examples/config.example.yaml \
  --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354 \
  --upstream 1.1.1.1:53
```

Smoke test in other terminal:

```bash
dig @127.0.0.1 -p 5354 example.com         # forwarded
dig @127.0.0.1 -p 5354 ads.example         # NXDOMAIN (sample blocklist)
curl http://127.0.0.1:9090/metrics | grep bns_
```

Reload blocklists in place: `pkill -HUP -f bin/bns`.

## Tech stack — non-obvious

- **DNS library: `codeberg.org/miekg/dns` v2** (see API cheat sheet below). NOT `github.com/miekg/dns` (v1, unmaintained). When fetch docs, use Context7 / codeberg, not stale v1 docs.
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

- `metrics` (outermost): times every query, records `bns_queries_total{outcome,qtype}` + duration histogram, recover panics into SERVFAIL + `bns_panics_total++`. Install block marker into ctx.
- `qlog`: emit one JSON line per query (qname, qtype, outcome, duration_ms; plus `client` host:port and `proto` udp/tcp when the handler installs `ClientInfo` in ctx) — no-op when `query_log.enabled=false`.
- `block`: `Holder.Current().Match(qname)` — on hit, call `resolver.MarkBlocked(ctx)` and synth NXDOMAIN (set Response=true, Rcode=NameError, copy ID + Question).
- `cache`: lookup by `cache.Key(q)`; hit return deep-copy with TTLs decremented by age; miss call next, compute per-RR min TTL (capped by `cfg.Cache.MaxTTL`, negative-cache use SOA Minttl capped by `NegativeTTLMax`), store deep-copy.
- `coalesce`: `singleflight.Group.Do` keyed by `cache.Key`; piggyback callers get `cache.CloneMsg` of originator response; `shared==true` increments `bns_coalesced_queries_total`.
- `forward` (terminal): hand req to configured `upstream.Upstream` (the Pool).

### Key interfaces (extensibility seams)

| Interface              | Where                       | Contract                                                           | Future impls                       |
| ---------------------- | --------------------------- | ------------------------------------------------------------------ | ---------------------------------- |
| `resolver.Resolver`    | `internal/resolver`         | `Resolve(ctx, req) (*dns.Msg, error)` — never mutate req, never return both | DNSSEC validator, ECS, rewrites |
| `upstream.Upstream`    | `internal/upstream`         | `Exchange(ctx, req) (*dns.Msg, error)`                              | DoH / DoT / DoQ clients           |
| `blocklist.Source`     | `internal/blocklist`        | `Load(ctx) ([]string, error)` — return canonical lowercased FQDNs   | URL fetcher with refresh ticker   |
| `logging.QueryLog`     | `internal/logging`          | `LogQuery(attrs ...slog.Attr)` — no-op when disabled                | (stable)                          |
| `cache.MetricsObserver`| `internal/cache`            | `SetEntries(int)`, `IncEvictions()` — wired via `metrics.CacheObserver()` | (stable)                    |

### Architecture invariants

Preserve when design:

- **Two listeners** (UDP + TCP) share one `dns.Handler`. Forwarder protocol pluggable via `Upstream` interface.
- **Blocklist match** is exact + subdomain wildcard. `ads.com` block `ads.com` and `*.ads.com`. Capacity target up to 1M entries (hagezi `pro.txt` ~471K today).
- **Cache** is in-memory, bounded, TTL-aware, lazy expiry on Get, LRU eviction on Store-when-full. Negative cache use SOA MIN capped by `NegativeTTLMax`. Not persistent.
- **Ownership of `*dns.Msg`**: cache and coalesce both deep-copy on store AND on get. miekg server pools `*Msg` — aliasing cached value with live request corrupts both. Use `cache.CloneMsg`, NOT `m.Copy()` (shallow on RR slices).
- **Upstream dedup** via `singleflight` (impl detail of `coalesce` package — don't reference singleflight from outside).
- **Blocklist atomic swap**: `Holder` wraps `atomic.Pointer[Matcher]`. Readers do lock-free `Current().Match(qname)`. SIGHUP reload calls `Loader.Load`, builds fresh `*Matcher`, `Holder.Swap(next)`.
- **Server start/shutdown race**: `dns.Server.init` writes internal state inside `ListenAndServe`. Wait on `Server.Ready(ctx)` before declaring listeners up or invoking Shutdown.
- **Resolver Resolve contract**: return `(resp, nil)` or `(nil, err)`, never `(resp, err)`. Impls must not mutate req. Block stage and Handler synth responses with `Response=true`, `ID=req.ID`, `Question=req.Question`.
- **Outcome label** carried via ctx-value (`resolver.MarkBlocked(ctx)` from blockstage; `resolver.Outcome(ctx, resp, err)` from metric + qlog stages) — keep "blocked" (synthetic) distinct from "nxdomain" (upstream).
- **Thread-safety required everywhere** — listeners, cache, blocklist, metrics all hit concurrently. Run `make race` before declare done.
- **Query log off by default**; gated behind `logging.query_log.enabled`.

## Performance posture

Target: few thousand QPS on Raspberry Pi-class hardware. NOT high-scale.

- Be allocation-conscious on hot path (query handle → cache lookup → forward → respond). Reuse buffers where library allows.
- Mind GC pauses — large blocklist structures allocated once at load, not churned.
- **No pre-optimize.** YAGNI/KISS. Profile with `pprof` first; only optimize when measurement justifies it.

## Development practices (enforced by user)

- **TDD, red-green-refactor.** Write failing test first.
- Idiomatic Go. Boring, well-trodden patterns over clever ones.
- Document public *and* internal interfaces. Extra care on load-bearing or non-obvious logic — explain *why*.
- Designs/specs belong in `docs/specs`, get committed.
- Impl plans belong in `docs/plans`, get committed.
- Build binaries to `bin/` (gitignored). Example: `go build -o bin/bns ./cmd/bns`.

## miekg/dns v2 — API cheat sheet

Import path is `codeberg.org/miekg/dns` (NO `/v2` suffix despite being v2). Field/function differences vs v1 that bit us:

- `Msg.ID` (uppercase), not `Id`.
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
- `Msg.Copy()` shallow on RR slices. For pool-safe deep copies use `cache.CloneMsg` which clones each RR via `rr.Clone()`.
- TC=1 truncation does NOT auto-retry — caller dials TCP explicit (see `internal/upstream/udp_client.go`).
- **RR construction shape changed.** Header is `dns.Header` (not `dns.RR_Header`), TTL is uppercase (no `Ttl`), no `Rrtype` field on Header. `dns.A`/`AAAA`/`NS`/`SOA` embed `rdata.X` sub-struct: `dns.A{Hdr: dns.Header{...}, A: rdata.A{Addr: netip.MustParseAddr("1.2.3.4")}}`. Field accessors work via promotion (`a.Hdr.TTL`, `ns.Ns`) but construction needs the sub-struct.

Vendored offline reference: `/home/ben.guest/vendor/miekg-dns-v2/`.

## Local vendored references

- `/home/ben.guest/vendor/miekg-dns-v2/` — codeberg miekg/dns v2 source. Read for API confirm before guess.
- `/home/ben.guest/vendor/hagezi-dns-blocklists/` — hagezi lists. Canonical format is `domains/` (newline-delimited FQDNs, `#` header comments only, punycode IDNs). Files big — never `Read` whole thing; use `head`/`wc -l`/`grep`.

## Container

- Image: `ghcr.io/bcrisp4/bns` — multi-arch (`linux/amd64`+`linux/arm64`), `gcr.io/distroless/static-debian12:nonroot`. ~10MB.
- Build: `deploy/docker/Dockerfile` (3 stages — hagezi-fetch on `$BUILDPLATFORM`, Go cross-compile via `GOOS/GOARCH` from `TARGETOS/TARGETARCH`, distroless runtime).
- CI: `.github/workflows/docker.yml` — buildx multi-arch + GHCR push on `main` + `v*` tags.
- **Container listens `:5354`** — nonroot uid 65532 cannot bind privileged ports; distroless has no `setcap` so `CAP_NET_BIND_SERVICE` route unavailable. Host maps `-p 53:5354/udp -p 53:5354/tcp`.
- Hagezi `pro.txt` baked at `/etc/bns/blocklists/pro.txt`, pinned via `HAGEZI_TAG` ARG in Dockerfile. Bump ARG to refresh.
- Config baked at `/etc/bns/config.yaml` (source: `deploy/docker/config.yaml`). Override at runtime via bind-mount, `BNS_*` env vars, or trailing CLI flags after image name.
- Reload blocklist without restart: `docker kill -s HUP <container>`.

## Build and test

- `make build` — `bin/bns` static binary
- `make test` — go test ./...
- `make race` — race detector (preferred — concurrency load-bearing)
- `make vet` — go vet
- `make lint` — golangci-lint (NOT installed locally; CI only)
- `make tidy` — go mod tidy

## Gotchas

- **Port 5353 is mDNS.** avahi-daemon / systemd-resolved listens on UDP/5353; bind there give `address already in use` on UDP only (TCP bind succeeds, mask cause). Use 5354 or other free port.
- **`serve` cobra flags minimal** — `--config/-c`, `--listen.udp`, `--listen.tcp`, `--upstream`, `--pprof`. Everything else (log level, query log, cache capacity, blocklist sources) go via env (`BNS_*`, `__` for nesting) or YAML. Note: env vars cannot index slice config (see viper gotcha below).
- **viper `AutomaticEnv` does NOT index slices via env vars.** `BNS_BLOCKLISTS__SOURCES__0__PATH` is ignored — viper never enumerates env keys; it only checks env on `Get("key.path")` calls, and slice unmarshal does not iterate indexed env. Slice config (blocklists.sources, upstreams) must come from YAML (`-c file.yaml`) or a CLI flag.
- **Manual smoke leaves bns on `:9090`.** Background-launched bns from a manual test isn't killed when the shell command ends. Check `ss -ltnp | grep :9090` and kill before re-running another bns.
- **Outcome label divergence from spec**: `bns_queries_total{outcome}` ∈ `{blocked, nxdomain, forwarded, error}` — NOT spec's `{hit, miss, blocked, error}`. Cache hit/miss not propagated; derive cache hit rate from `bns_upstream_queries_total` vs `bns_queries_total`.
- **GHCR package visibility inherits from repo on first push.** Public repo → public package automatically; no manual flip. Local `gh` token here lacks `read:packages`/`write:packages` scopes — query via anonymous `docker pull` (after `docker logout ghcr.io`), not the GHCR REST API.
- **Dev host is arm64.** `docker buildx build` default platform = arm64; pass `--platform linux/amd64` explicitly for cross-arch smoke. Multi-arch `buildx --load` not supported (single-arch only) — verify multi-arch via CI or push to registry.

## Stress harness

`cmd/bns-stress` orchestrates `mockupstream` + `bns serve` + dnspyre (imported as library). One run produces `dist/stress/<RFC3339>/{report.md,*.pprof,*.prom}`.

```
make build-stress
./bin/bns-stress --scenario mixed --duration 60s --concurrency 50 \
  --blocklist /home/ben.guest/vendor/hagezi-dns-blocklists/domains/pro.txt \
  --max-io-errors 200
```

Spec: `docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md`. Note: dnspyre's `@<path>` query-list syntax is kingpin CLI sugar — library callers (i.e. orchestrator) must expand themselves. `--max-io-errors` absorbs the dnspyre tail-cancellation artefact in short runs.