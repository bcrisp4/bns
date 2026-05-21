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

## Release automation

- **Auto-create GitHub Release on `v*` tag push.**
  Today `.github/workflows/docker.yml` builds + pushes the multi-arch
  image on `v*` tags but does NOT create a matching GitHub Release —
  done by hand via `gh release create`. Add a small job (separate
  workflow or step in docker.yml) that runs on `push.tags: ['v*']` and
  calls `gh release create ${{ github.ref_name }} --generate-notes`
  using `GITHUB_TOKEN` (needs `contents: write`). Optional upgrade
  path: replace with `goreleaser` if/when binary artefacts are wanted
  alongside the container image (cross-compile via `goreleaser` covers
  the same arches the Dockerfile does).

## BNS CLI surface

- **Resolve scenario `@<path>` entries against a known root.**
  `internal/stress/orchestrator.go:expandFileRefs` opens the path as
  given. Today that is implicitly relative to `bns-stress`'s cwd, which
  works because `make stress` runs from the repo root. A user
  invoking `./bin/bns-stress` from anywhere else gets a confusing
  open error. Either resolve relative to the binary or embed the
  queries with `embed.FS` (and let scenarios choose).

## Blocklist source extensions (2026-05-21-bns-http-blocklist-source)

Deferred from the HTTP-blocklist-source spec (§10). Each is independently
schedulable and does not block the others.

- **Allowlist / per-domain override.** Curated lists like hagezi can
  false-positive on legitimate domains. Add an `allowlist:` block whose
  matches short-circuit the block stage before the matcher.
- **Tiered hagezi flavour selection.** `light` / `multi` / `pro` /
  `pro.plus` / `ultimate`. A single config key that resolves to the
  right URL; saves operators from copy-pasting URLs.
- **Integrity / sha256 verification.** Hagezi publishes `.sha256`
  siblings. Optional `sha256_url:` per source; the fetcher refuses to
  swap cache if the hash mismatches.
- **Per-source refresh intervals.** Today's global
  `blocklists.refresh_interval` is fine when one URL dominates. Add
  `sources[].refresh_interval:` override when a second URL has different
  freshness needs.
- **Configurable failure-mode policy.** Today: always fail-open. Add
  `on_failure: {fail_open, fail_start, fail_after: 7d}` per source for
  stricter operators.
- **Force-refresh admin HTTP endpoint.**
  `POST /admin/refresh-blocklists` so operators can poke without `kill
  -HUP` capability. Needs admin-auth design first.
- **Source types beyond `file` and `http`.** FTP, S3, `gs://`, git. Each
  implements `Source` + (for fetched sources) provides its own fetcher;
  the `type:` discriminator already supports it.
- **Fetch latency histogram metric.**
  `bns_blocklist_fetch_duration_seconds` when fetch latency becomes
  interesting (today's 1-fetch-per-day default makes the histogram noise
  more than signal).
- **Default-list curation.** Revisit which list ships in the default
  container config (currently hagezi `pro`). Consider shipping a slim
  "ads + trackers only" list as the default and documenting `pro` as an
  opt-in.
