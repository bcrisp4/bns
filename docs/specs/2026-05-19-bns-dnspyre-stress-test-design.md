# BNS stress-test harness — design

- Date: 2026-05-19
- Author: Ben Crisp (brainstormed with Claude Code)
- Status: Draft, awaiting user review
- Branch: `main`

## 1. Purpose

Provide a reproducible stress-test harness for BNS that supports three goals over time:

1. **Bottleneck hunting.** Saturate the resolver chain, capture pprof + Prometheus metrics, surface contention, allocation hot spots, or slow stages.
2. **Baseline + regression visibility.** Capture a stable headline number (sustained QPS, p50/p95/p99, allocation counters) per scenario per run so future changes can be compared by eye.
3. **Target validation.** Demonstrate the "few thousand QPS on Raspberry-Pi-class hardware" target documented in `CLAUDE.md`.

Today the harness ships one scenario (`mixed`) and soft-target reporting. Sibling scenarios and hard regression gates are explicitly YAGNI; the harness is structured to absorb them later.

## 2. Non-goals

- No SSH automation. Remote (Pi) runs are user-driven: the user SSHes to the Pi and runs `make stress`, or starts BNS on the Pi and points the orchestrator at it from a workstation.
- No load-generator-as-service. Each run is a single invocation that produces one artefact directory.
- No comparison/regression tooling in this iteration. Reports are human-readable; diffs are eyeballed.
- No production behavioural changes beyond a single opt-in pprof flag (`bns serve --pprof`).
- No new public API surface on the production packages under `internal/`.

## 3. Topology

Three processes cooperate per run. The orchestrator owns the lifecycle.

```
+----------------+    queries     +-------------------+    forwards   +-------------------+
|  dnspyre       | -------------> |  bns (under test) | ------------> |  mockupstream     |
|  (in-process,  | <------------- |  :5354 udp+tcp    | <------------ |  127.0.0.1:5355   |
|   imported)    |   responses    |  :9090 /metrics   |   instant     |  miekg/dns server |
+----------------+                |  :6060 /pprof     |               +-------------------+
        ^                         +-------------------+
        |                                  ^
        +---- bns-stress orchestrator -----+
              spawns mock + bns (or skips if --target is remote),
              scrapes /metrics before+after, captures pprof,
              calls dnsbench.Benchmark.Run(ctx), writes report
```

dnspyre is consumed as a **library** (`github.com/tantalor93/dnspyre/v3/pkg/dnsbench`), not as an external binary, so the harness ships as a single hermetic Go binary with no `$PATH` dependency. This matches BNS's single-static-binary aesthetic and the Pi deploy story. Subprocesses remain for `bns serve` and `mockupstream` so that the BNS↔upstream wire path stays measurable end-to-end (allocations and (de)serialisation are not optimised away by an in-process upstream).

Two supported topologies, both driven by the orchestrator's `--target` flag:

- **All-on-one host (default).** Orchestrator spawns `mockupstream` and `bns serve`, then drives dnspyre against `127.0.0.1`. Generator and target share CPU; numbers are directional, fine for local iteration and for Pi smoke runs.
- **Generator-host → BNS-host.** User runs `bns serve` and `mockupstream` on a target host manually (or via `make stress-server`). The orchestrator runs on a generator host with `--target=<host>:5354 --no-spawn --admin=<host>:9090`. `/metrics` and pprof are scraped over the network from the admin port; both work remotely. The only thing skipped is process supervision.

## 4. Components

### 4.1 `cmd/mockupstream/` (new binary)

A trivial DNS server used as BNS's upstream during stress runs.

- Listens UDP + TCP on `--listen.udp` / `--listen.tcp` (defaults `127.0.0.1:5355`).
- Per-qtype canned answers:
  - `A` → `192.0.2.1`, TTL = `--ttl` (default `300s`)
  - `AAAA` → `2001:db8::1`, TTL = `--ttl`
  - `NS` → `ns.mock.invalid.`, TTL = `--ttl`
  - `SOA` → minimal SOA with `MINIMUM = --neg-ttl` (default `60s`). Exercises BNS's negative-cache path.
  - anything else → `REFUSED`.
- Optional knobs reserved for future scenarios: `--latency 0ms` (sleep before reply), `--drop-rate 0.0` (silent drop fraction).
- Uses `codeberg.org/miekg/dns` (v2) and the same `NotifyStartedFunc` ready-sync pattern as `internal/server/server.go`, so the orchestrator can wait deterministically before launching load.
- `slog` logs one startup line and one shutdown summary line. No per-query logging (would dominate at 10k QPS).
- Built only via `make build-stress`. Not included in any release artefact.

### 4.2 `cmd/bns-stress/` (new binary)

Single orchestrator binary. Uses `flag` (stdlib) — boring, no cobra.

CLI surface:

```
bns-stress
  --scenario      mixed                  # only scenario today
  --duration      60s
  --concurrency   50
  --rate-limit    0                      # 0 = unbounded; passed to dnspyre Rate
  --target        127.0.0.1:5354         # dnspyre target; if local, orchestrator spawns BNS
  --admin         127.0.0.1:9090         # admin endpoint (metrics + pprof); defaults to --target host : 9090
  --no-spawn                             # set when pointing at an externally-running BNS
  --bns-bin       ./bin/bns
  --mock-bin      ./bin/mockupstream
  --blocklist     /home/ben.guest/vendor/hagezi-dns-blocklists/dnsmasq/pro.txt
  --out           dist/stress/<RFC3339>/
  --pprof-cpu     30s
  --pprof-heap                            # collect heap snapshot post-run
```

Run sequence:

1. Resolve config, create `--out` directory (`dist/stress/<RFC3339>/`).
2. If `--target` is local-loopback and `--no-spawn` is unset:
   - Spawn `mockupstream`; wait for ready (notify or 5s timeout).
   - Spawn `bns serve` with scenario-supplied env (`BNS_BLOCKLISTS__SOURCES__0__PATH`, etc.) and CLI flags (`--upstream 127.0.0.1:5355`, `--listen.udp/tcp` from `--target`, `--pprof`).
   - Poll `/readyz` every 50ms; fail after 5s.
3. `GET <admin>/metrics` → `before.prom`.
4. Kick off pprof captures (`GET <admin>/debug/pprof/profile?seconds=N` → `cpu.pprof`) in background; arrange heap snapshot (`GET <admin>/debug/pprof/heap`) at end of run. Works over the network — admin host need not be local.
5. Build `dnsbench.Benchmark{}` from the scenario, set `Writer=io.Discard`, `Silent=true`, `ProgressBar=false`, `JSON=false`, `Server=<target>`, `Duration`, `Concurrency`, then call `results, err := b.Run(ctx)`.
6. `GET /metrics` → `after.prom`.
7. Compute metrics diff (per §6).
8. Re-render dnspyre's own JSON output for archival: temporarily set `b.JSON=true`, `b.Writer=<jsonFile>` and call `reporter.PrintReport(&b, results, start, dur)` → `dnspyre-results.json`.
9. Render `report.md` (see §6) from typed `*dnsbench.ResultStats` + metrics diff.
10. Send `SIGTERM` to `bns`, wait 10s, `SIGKILL` if needed; then `mockupstream`.
11. Print a single summary line to stdout.

Failure handling: any subprocess exiting non-zero before completion → kill peers, write `FAILED` marker file containing the error, exit 1. Partial reports are never claimed as successful.

### 4.3 `internal/stress/` (new package)

```
internal/stress/
  scenario.go       # type Scenario, registry
  scenarios/
    mixed.go        # the one scenario shipped today
  metrics.go        # Prometheus snapshot parse + diff helper
  report.go         # report.md template + render
  orchestrator.go   # spawn lifecycle, pprof capture, run sequencing
```

`Scenario` struct:

```go
type Scenario struct {
    Name          string
    BlocklistPath string                // overrides --blocklist if set
    BNSEnv        map[string]string     // additional BNS_* env vars
    Build         func(target string, dur time.Duration, c uint32) dnsbench.Benchmark
}
```

The package is single-purpose; it does not import `internal/resolver`, `internal/cache`, or `internal/blocklist`. It depends only on the public `dnsbench` + `reporter` packages from dnspyre and on `net/http` for `/metrics` scraping.

### 4.4 `scripts/stress/queries/` (new directory)

- `mixed.txt` — ~10 000 names total:
  - 50 cache-hot names: `hot-0001.test` … `hot-0050.test`
  - 9 000 cold names: `cold-0001.test` … `cold-9000.test`
  - 950 blocked names: a frozen subset of `pro.txt` from `/home/ben.guest/vendor/hagezi-dns-blocklists/`. The subset is checked in to keep runs reproducible across hagezi updates.
- `blocked-sample.txt` — the source-of-truth for the blocked subset; `mixed.txt` is generated from a small build script (`scripts/stress/build_mixed.sh`) for documentation, but the generated file is what gets committed and used by the harness.

### 4.5 Production changes (minimal, opt-in)

- `cmd/bns/serve.go`: new `--pprof` boolean flag (default `false`). When set, the `net/http/pprof` handlers are mounted on the existing admin mux from `internal/admin/`. Off by default, matching the "query log off by default" posture.

No other production touch. The harness is observation-only on BNS under test.

## 5. Scenario shipped today: `mixed`

Composition (from `scripts/stress/queries/mixed.txt`):

- 50 cache-hot names + 9 000 cold names + 950 blocked names.
- dnspyre fans across two query types (`A`, `AAAA`) per name, doubling the total query count and matching dual-stack reality.

dnspyre invocation, expressed as `dnsbench.Benchmark` fields:

```go
dnsbench.Benchmark{
    Server:        target,
    Types:         []string{"A", "AAAA"},
    Concurrency:   c,
    Duration:      d,
    Probability:   0.7,                          // randomise across workers
    Queries:       []string{"@scripts/stress/queries/mixed.txt"},
    Recurse:       true,
    Writer:        io.Discard,
    Silent:        true,
    ProgressBar:   false,
    JSON:          false,                        // we render our own report
    Rcodes:        true,
    HistDisplay:   false,
    HistPre:       dnsbench.DefaultHistPrecision,
}
```

dnspyre's `--fail` correctness gates are wired by the orchestrator post-run via the typed `reporter.Merge(&b, results).Counters`:

- `Counters.IOError > 0` → exit 1
- `Counters.IDmismatch > 0` → exit 1

Latency, negative answers, and DNS error rcodes are recorded as information; they do not fail the run today. This matches the "soft latency, hard correctness" framing.

BNS configuration under test:

- Cache: capacity `10000`, `max_ttl=86400s`, `negative_ttl_max=900s` (defaults from `examples/config.example.yaml`).
- Blocklist: full hagezi `pro.txt` (~471k entries) loaded once at start. Stresses `Matcher` at realistic capacity.
- Query log: disabled. Log level: `info`.
- Admin: enabled on `:9090`. pprof: enabled (`--pprof`).

## 6. Output artefacts

Per-run directory `dist/stress/<RFC3339-ts>/`. The `dist/` directory is added to `.gitignore`.

```
dist/stress/2026-05-19T14-22-03Z/
  config.json            # resolved orchestrator config + bns git sha + go version + host
  bns.log                # bns stdout/stderr (slog JSON lines)
  mockupstream.log       # mock stdout/stderr
  dnspyre-results.json   # raw dnspyre JSON, produced via reporter.PrintReport
  before.prom            # GET /metrics pre-run
  after.prom             # GET /metrics post-run
  metrics-diff.txt       # human-readable delta of key counters + histograms
  cpu.pprof              # 30s CPU profile during run (from --admin host)
  heap.pprof             # heap snapshot post-run (from --admin host)
  report.md              # rendered headline summary
  FAILED                 # present only on failure; contains the error
```

`metrics-diff.txt` includes:

- `bns_queries_total{outcome=forwarded|blocked|nxdomain|error}` deltas + percentages.
- `bns_upstream_queries_total` delta (used to compute cache-miss rate).
- `bns_coalesced_queries_total` delta.
- `bns_cache_evictions_total` delta.
- `bns_panics_total` delta (expected zero).
- p50 / p95 / p99 / p99.9 computed from `bns_query_duration_seconds_bucket` deltas via cumulative-histogram quantile interpolation.

`report.md` is a fixed Markdown template populated from `dnsbench.ResultStats` + metrics diff. Headline table at the top, breakdowns underneath, pprof file pointers at the bottom. Stable byte-for-byte for a given input fixture so it can be unit-tested.

Final stdout line, one per run:

```
mixed 60s c=50 QPS=12,431 p99=1.8ms errors=0 blocked=14.2% report=dist/stress/2026-05-19T14-22-03Z/report.md
```

## 7. Testing strategy

Per the project's TDD posture, the harness itself is treated as a normal package and gets tests before code lands.

Unit tests under `internal/stress/`:

- `scenario_test.go` — each registered `Scenario.Build` produces the expected `dnsbench.Benchmark` field set for fixed inputs (golden compare).
- `metrics_test.go` — `.prom` snapshot parsing + diff against fixture pairs.
- `report_test.go` — template renders byte-stable for a fixed input fixture.

Mock upstream tests under `cmd/mockupstream/`:

- Spawn in-process; assert canned A/AAAA/SOA/NS answers, REFUSED for unknown qtypes, correct TTL.
- `--drop-rate 1.0` causes BNS-side timeout (via a tiny BNS-shaped client in the test).
- `--latency 50ms` is observable within a reasonable tolerance.

Orchestrator integration test under `cmd/bns-stress/main_test.go`, build tag `stress_integration` (off in default `go test`):

- `TestMain` runs `go build` for `bin/bns`, `bin/mockupstream`, `bin/bns-stress`.
- Runs a 2-second mini-scenario at concurrency 4 against `127.0.0.1`.
- Asserts: exit 0, `report.md` exists and parses, `dnspyre-results.json` non-empty, no `FAILED` marker, no leaked child processes.

Race detector: every new package is `make race`-clean. The orchestrator spawns + reaps subprocesses concurrently with metrics polling, so this is not optional.

## 8. Makefile additions

```make
.PHONY: build-stress stress stress-server stress-clean

build-stress: build
	$(GO) build -trimpath -o $(BIN_DIR)/mockupstream ./cmd/mockupstream
	$(GO) build -trimpath -o $(BIN_DIR)/bns-stress   ./cmd/bns-stress

stress: build-stress
	./$(BIN_DIR)/bns-stress --scenario mixed --duration 60s --concurrency 50

stress-server: build
	@echo "Run mockupstream and bns serve manually for the remote-target topology."
	@echo "See docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md §3."

stress-clean:
	rm -rf dist/stress
```

`make build` and the rest of the existing target set are untouched. `cmd/mockupstream` and `cmd/bns-stress` are not built by `make build` and are not shipped in release artefacts.

## 9. Dependencies

Added to `go.mod` (only affects `cmd/bns-stress` and `internal/stress` import graphs; `cmd/bns` does not import them):

- `github.com/tantalor93/dnspyre/v3 vX.Y.Z` — pinned at the latest tagged release at implementation time. As of this writing: `v3.11.0`.

Transitively this pulls in ~25 packages (`hdrhistogram`, `quic-go`, `kingpin/v2`, `montanaflynn/stats`, `olekukonko/tablewriter`, `fatih/color`, `schollz/progressbar/v3`, `tantalor93/doh-go`, `tantalor93/doq-go`, `go-hep.org/x/hep`, `go.uber.org/ratelimit`, and the standard supporting tree). None of them land in the `cmd/bns` import closure; `go mod why` should confirm at PR time.

dnspyre depends on `github.com/miekg/dns` v1; BNS depends on `codeberg.org/miekg/dns` v2. They are different modules; Go's module system tolerates the coexistence cleanly. The harness never crosses the wire between the two — dnspyre's request/response types live entirely inside `dnsbench.Benchmark.Run`.

## 10. Decision log

- **Why dnspyre as a library, not a subprocess?** Hermetic build, typed results, native `ctx` cancellation, no `$PATH` setup on the Pi. Module-graph bloat is accepted as the cost.
- **Why a separate `mockupstream` subprocess, not in-process?** Keeps the BNS↔upstream wire path (UDP/TCP, packing/unpacking) in the measurement. An in-process upstream would optimise away allocations we care about.
- **Why no SSH automation?** Adds engineering for a workflow the user already has. Same binary, two topologies, user decides.
- **Why one scenario today?** YAGNI. Mixed gives the most realistic single signal; sibling scenarios (`cache-hot`, `cache-cold`, `blocked-only`) are cheap to add later because `Scenario` is already pluggable.
- **Why soft targets, not a hard QPS gate?** No baseline data exists yet; an arbitrary number would either flake CI or paper over regressions. Correctness flags (`ioerror`, `idmismatch`) are gated because they should always be zero.
- **Why a new `--pprof` flag on `bns serve` instead of always-on pprof?** Same posture as the query log — debug surfaces stay opt-in.
- **Why output to `dist/stress/`?** Matches existing `bin/` and `dist/` conventions; gitignored; per-run timestamped dirs avoid clobbering.

## 11. Open follow-ups (not in scope here)

- Sibling scenarios (`cache-hot`, `cache-cold`, `blocked-only`) as separate `Scenario` registrations.
- A `bns-stress diff <old-dir> <new-dir>` subcommand once enough baselines exist to make comparison meaningful.
- CI integration: nightly run on a dedicated runner, archiving `dist/stress/<ts>/` as a build artefact. Soft targets become hard once we have data.
- Pi-specific scenario tuning (lower concurrency, lower duration) if shared-CPU contamination shows up obviously in reports.
- A `--latency` / `--drop-rate` driven scenario to characterise BNS behaviour under a flaky upstream.
