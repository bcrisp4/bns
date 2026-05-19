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

- **Resolve scenario `@<path>` entries against a known root.**
  `internal/stress/orchestrator.go:expandFileRefs` opens the path as
  given. Today that is implicitly relative to `bns-stress`'s cwd, which
  works because `make stress` runs from the repo root. A user
  invoking `./bin/bns-stress` from anywhere else gets a confusing
  open error. Either resolve relative to the binary or embed the
  queries with `embed.FS` (and let scenarios choose).
