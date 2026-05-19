# TODO

Tracked follow-ups. Not bugs; not currently blocking. Promote to a spec/plan when picked up.

## Stress harness (2026-05-19-bns-dnspyre-stress-test)

- **Replace hand-rolled Prometheus parser with `prometheus/common/expfmt.TextParser`.**
  `internal/stress/metrics.go` has ~65 lines of bespoke line/label/`+Inf` parsing.
  `prometheus/common` is already a transitive dep via `client_golang`; switching
  drops ~50 lines and hardens against edge cases (escaping, comments,
  multi-value histograms). Caught by `/simplify` review.

- **Type the resolver outcome labels.**
  `"forwarded" / "blocked" / "nxdomain" / "error"` appear as raw string literals
  in `internal/resolver/metricstage`, `internal/stress/scenarios/mixed.go`,
  `internal/stress/metrics.go`, `internal/stress/report.go`, and
  `cmd/bns-stress/main.go`. A shared typed const in
  `internal/resolver` (or a new `outcomes` package) would catch
  misspellings at compile time and make label refactors safe.

- **Dedupe the test-only `findFreePort` helper.**
  `cmd/mockupstream/main_test.go` and `cmd/bns-stress/main_test.go` both
  reimplement the grab-ephemeral-port-then-close trick. Lift into
  `internal/upstream/testutil` (or a new `internal/testutil`) when a
  third caller appears; not worth the move for two.

- **Sibling scenarios (`cache-hot`, `cache-cold`, `blocked-only`).**
  Listed as YAGNI in the spec. Add when `mixed` numbers stop being
  enough to localise a regression. `Scenario` registry already supports
  the pluggability.

- **`bns-stress diff <old> <new>` subcommand.**
  No regression-gate utility today — comparison is eyeball-driven. Add
  once enough baseline reports exist to make it meaningful.

## BNS CLI surface

- **Add `--blocklist` flag to `bns serve` (repeatable).**
  Today blocklists must come from YAML config (viper's `AutomaticEnv`
  cannot index slices). The stress orchestrator works around this by
  writing a temp YAML. A repeatable `--blocklist <path>` flag would
  remove that workaround and make ad-hoc invocations less awkward.
  Build the slice in `runServe` from the flag values, appended to
  `cfg.Blocklists.Sources` after viper unmarshal.

- **Audit `internal/config/config.go` for fields that should also be
  CLI flags.** Today only `--listen.udp`, `--listen.tcp`, `--upstream`,
  `--pprof`, `-c` exist on `serve`. Other config keys (`cache.capacity`,
  `cache.max_ttl`, `cache.negative_ttl_max`, `logging.level`,
  `logging.query_log.enabled`, `admin.listen`, `shutdown_timeout`,
  `startup_probe_timeout`) require either YAML or `BNS_*` env vars.
  Per project preference: expose everything as a flag where it
  makes sense, with env + YAML still available as fallbacks (precedence
  is already `flag > env > YAML > defaults`). One pass through the
  config struct, one binder block. Slice fields (`upstreams`,
  `blocklists.sources`) need bespoke handling per above.

- **Resolve scenario `@<path>` entries against a known root.**
  `internal/stress/orchestrator.go:expandFileRefs` opens the path as
  given. Today that is implicitly relative to `bns-stress`'s cwd, which
  works because `make stress` runs from the repo root. A user
  invoking `./bin/bns-stress` from anywhere else gets a confusing
  open error. Either resolve relative to the binary or embed the
  queries with `embed.FS` (and let scenarios choose).
