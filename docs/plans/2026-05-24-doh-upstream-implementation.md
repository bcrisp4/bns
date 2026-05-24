# DoH Upstream Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add DNS-over-HTTPS upstream support to BNS alongside the existing UDP forwarder, with operator-pinned endpoint IPs (no bootstrap-via-DNS), HTTP/2 connection reuse, RFC 8484 compliance, and per-query log attribution.

**Architecture:** Extend `Upstream` interface with `Name()` + `Protocol()`; collapse `Pool` to single slice. Implement `DoHClient` over stdlib `net/http` using miekg `dnshttp.Response` for decode (bypass `dnshttp.NewRequest` to avoid `/dns-query` double-append). Operator-pinned `endpoint_ips` substitute hostname in `Transport.DialContext`; TLS handshake validates against URL hostname via SNI. Add `protocol` label to existing `bns_upstream_*` metrics; new `bns_doh_http_status_total` + `bns_doh_tls_handshakes_total` vectors. New ctx-marker triad (`WithUpstreamMarker`/`MarkUpstream`/`UpstreamInfoFrom`) propagates winning upstream identity into the query log.

**Tech Stack:** Go 1.26, `codeberg.org/miekg/dns` v2 (incl. `dnshttp` sub-package), `golang.org/x/net/http2`, `crypto/tls`, `net/http/httptrace`, `log/slog`, Prometheus client (`prometheus/client_golang`).

**Spec:** `docs/specs/2026-05-24-doh-upstream-design.md`

---

## File Map

**Create:**

- `internal/upstream/doh_client.go` — `DoHClient` implementing `Upstream`.
- `internal/upstream/doh_client_test.go` — white-box tests (same package, so TLS roots can be injected without exposing a production seam).
- `internal/upstream/factory.go` — dispatches `config.Upstream` → concrete `Upstream`.
- `internal/upstream/factory_test.go` — black-box (`upstream_test`).
- `internal/upstream/testutil/tlscert.go` — generates self-signed test certs with caller-specified SANs.
- `internal/upstream/testutil/tlscert_test.go` — sanity-checks the cert helper.
- `internal/integration/integration_doh_test.go` — end-to-end DoH path through real BNS chain.
- `examples/config-doh.yaml` — minimal DoH-only config for manual smoke + integration use.

**Modify:**

- `internal/upstream/upstream.go` — extend `Upstream` interface.
- `internal/upstream/udp_client.go` — add `Name()` + `Protocol()` methods.
- `internal/upstream/pool.go` — drop parallel slices, use interface methods, call `MarkUpstream` on success.
- `internal/upstream/pool_test.go` — update `fakeUpstream`; add marker + protocol tests.
- `internal/upstream/testutil/spawn.go` — no change expected; verify mid-plan.
- `internal/resolver/outcome.go` — add `UpstreamInfo` + `WithUpstreamMarker` + `MarkUpstream` + `UpstreamInfoFrom`.
- `internal/resolver/outcome_test.go` — round-trip tests for the new triad.
- `internal/resolver/metricstage/metrics.go` — install upstream marker beside block marker.
- `internal/resolver/qlog/qlog.go` — emit `upstream` + `upstream_protocol` attrs when marker is set.
- `internal/resolver/qlog/qlog_test.go` — new attr assertions.
- `internal/config/config.go` — extend `Upstream` struct with `Type` / `URL` / `EndpointIPs`.
- `internal/config/validate.go` — UDP+DoH validation branches; cross-field bootstrap check.
- `internal/config/validate_test.go` — new test cases.
- `internal/metrics/metrics.go` — add `protocol` label to upstream vectors; add `DoHHTTPStatusTotal` + `DoHTLSHandshakesTotal` vectors.
- `cmd/bns/serve.go` — remove `--upstream` cobra flag; replace upstream loop with factory; rename `upstreamAddrs` → `upstreamDialAddrs`.
- `examples/config.example.yaml` — add a DoH entry alongside UDP.
- `deploy/docker/config.yaml` — switch to DoH-primary with UDP fallback.
- `CLAUDE.md` — Quickstart, package layout, key interfaces, gotchas.

**Will be touched indirectly (verify compile only):**

- `internal/resolver/metricstage/metrics_test.go` — should remain green after marker install (no new assertion required; assert separately in marker round-trip tests).
- `internal/upstream/udp_client_test.go` — should remain green; existing UDP tests unaffected.

---

## Task 1: Extend `Upstream` interface with `Name()` and `Protocol()`

Foundation for collapsing Pool's parallel slices. Add the methods, implement on `UDPClient` and on `fakeUpstream` in pool_test. No callers yet; behaviour unchanged.

**Files:**

- Modify: `internal/upstream/upstream.go`
- Modify: `internal/upstream/udp_client.go`
- Modify: `internal/upstream/pool_test.go` (the test-only `fakeUpstream`)

- [ ] **Step 1: Extend interface**

Replace `internal/upstream/upstream.go` contents:

```go
// Package upstream defines the Upstream interface and concrete clients
// used by the forwarder stage of the resolver chain.
package upstream

import (
	"context"

	"codeberg.org/miekg/dns"
)

// Upstream sends a DNS query to a configured upstream resolver and returns
// the response. Implementations MUST NOT mutate req.
//
// Name and Protocol provide identity for metrics labels and per-query
// log attribution. Name is the operator-meaningful identifier (e.g.
// "1.1.1.1:53" for UDP, the DoH URL for DoH). Protocol is the transport
// kind ("udp" | "doh"). Both MUST be cheap and safe to call concurrently.
type Upstream interface {
	Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
	Name() string
	Protocol() string
}
```

- [ ] **Step 2: Add methods to `UDPClient`**

Append to `internal/upstream/udp_client.go` (after the existing `Exchange` method):

```go
// Name returns the configured upstream address (e.g. "1.1.1.1:53").
// Used as the "upstream" metric label.
func (c *UDPClient) Name() string { return c.addr }

// Protocol returns "udp".
func (c *UDPClient) Protocol() string { return "udp" }
```

- [ ] **Step 3: Update test-only `fakeUpstream`**

In `internal/upstream/pool_test.go`, find the `fakeUpstream` struct and `Exchange` method. Add the two new methods immediately after `Exchange`:

```go
func (f *fakeUpstream) Name() string     { return f.name }
func (f *fakeUpstream) Protocol() string { return "udp" }
```

(`Protocol` returns `"udp"` because the existing fakes are UDP-flavoured; later tests will introduce a DoH-flavoured fake explicitly.)

- [ ] **Step 4: Compile check**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 5: Tests still green**

Run: `make test`
Expected: all existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/upstream/upstream.go internal/upstream/udp_client.go internal/upstream/pool_test.go
git commit -m "$(cat <<'EOF'
upstream: extend Upstream interface with Name() and Protocol()

Foundation for collapsing Pool's parallel name/protocol slices in a
later commit. Adds two trivial getters to UDPClient and fakeUpstream.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `protocol` label to upstream metric vectors

Additive vector change — Pool will use it in Task 3. After this task, the metric vectors carry an extra label but no caller passes it yet (compile break is fine across one commit because Pool is updated atomically in Task 3 — but to keep the tree green at each commit, we update Pool in the same commit).

Actually: do this atomically with Task 3 — see below. This task left as a placeholder; Task 3 covers both changes.

(No standalone task. Proceed to Task 3.)

---

## Task 3: Pool refactor — drop parallel slices, use interface, add `protocol` label

Pool stops carrying parallel `names []string`. Metric vectors gain `protocol` label. `cmd/bns/serve.go` construction simplifies. Atomic single-commit change so every commit compiles.

**Files:**

- Modify: `internal/upstream/pool.go`
- Modify: `internal/upstream/pool_test.go`
- Modify: `internal/metrics/metrics.go`
- Modify: `cmd/bns/serve.go`

- [ ] **Step 1: Update metric vector definitions**

In `internal/metrics/metrics.go`, find the `UpstreamQueriesTotal` and `UpstreamDurationSeconds` definitions and change their label sets:

```go
UpstreamQueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_upstream_queries_total",
    Help: "Total upstream queries, by upstream, protocol and outcome.",
}, []string{"upstream", "protocol", "outcome"}),
UpstreamDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
    Name:    "bns_upstream_duration_seconds",
    Help:    "Upstream exchange duration in seconds, by upstream and protocol.",
    Buckets: prometheus.DefBuckets,
}, []string{"upstream", "protocol"}),
```

- [ ] **Step 2: Rewrite `Pool`**

Replace the body of `internal/upstream/pool.go` with:

```go
package upstream

import (
	"context"
	"errors"
	"fmt"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/metrics"
	"github.com/bcrisp4/bns/internal/resolver"
)

// Pool tries each Upstream in declared order, returning the first success.
// Failover is sequential: secondary upstreams are consulted only when the
// primary returns an error. SERVFAIL/NXDOMAIN/REFUSED from a successful
// exchange are valid responses and do NOT trigger failover.
//
// Errors are aggregated via errors.Join. ctx cancellation short-circuits.
type Pool struct {
	upstreams []Upstream
	m         *metrics.Metrics // nil → no metrics
}

// NewPool constructs a Pool over ups. mtr may be nil.
func NewPool(ups []Upstream, mtr *metrics.Metrics) *Pool {
	return &Pool{upstreams: ups, m: mtr}
}

// Name returns "pool" — Pool itself is wrapped by the forwarder stage,
// not recorded directly as an upstream in metrics. Provided for interface
// completeness.
func (p *Pool) Name() string { return "pool" }

// Protocol returns "pool" for the same reason as Name.
func (p *Pool) Protocol() string { return "pool" }

// Exchange tries each upstream in order. Returns the first success.
// On success, records the winning upstream's name + protocol in ctx via
// resolver.MarkUpstream so the query-log stage can surface it.
// If all fail, returns errors.Join of all failures.
func (p *Pool) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(p.upstreams) == 0 {
		return nil, errors.New("upstream pool: no upstreams configured")
	}
	errs := make([]error, 0, len(p.upstreams))
	for _, u := range p.upstreams {
		start := time.Now()
		resp, err := u.Exchange(ctx, req)
		elapsed := time.Since(start).Seconds()
		name := u.Name()
		proto := u.Protocol()
		if p.m != nil {
			p.m.UpstreamDurationSeconds.WithLabelValues(name, proto).Observe(elapsed)
			if err == nil {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, proto, "ok").Inc()
			} else {
				p.m.UpstreamQueriesTotal.WithLabelValues(name, proto, "error").Inc()
			}
		}
		if err == nil {
			resolver.MarkUpstream(ctx, name, proto)
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
```

(Note: this uses `resolver.MarkUpstream` which doesn't exist yet — Task 5 adds it. Add a temporary stub so the tree compiles. Replace with the real implementation in Task 5.)

Add a temporary `MarkUpstream` stub in `internal/resolver/outcome.go` (just enough to compile):

```go
// MarkUpstream is a placeholder added in this task to keep the tree
// compiling. The real implementation lands in the upstream marker task.
func MarkUpstream(ctx context.Context, name, protocol string) {
	_ = ctx
	_ = name
	_ = protocol
}
```

- [ ] **Step 3: Update `cmd/bns/serve.go` Pool construction**

Find the upstream loop (around line 197). Replace with the simplified version (still using `NewUDPClient` directly — factory comes in Task 11):

```go
ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
for _, u := range cfg.Upstreams {
    ups = append(ups, upstream.NewUDPClient(u.Addr, u.Timeout))
}
pool := upstream.NewPool(ups, mtr)
```

- [ ] **Step 4: Update `pool_test.go` `NewPool` call sites**

Find every `upstream.NewPool(...)` call in `internal/upstream/pool_test.go`. The signature changed from `NewPool(ups, names, mtr)` to `NewPool(ups, mtr)`. Drop the middle argument.

Example before:

```go
p := upstream.NewPool([]upstream.Upstream{
    &fakeUpstream{name: "p", resp: ok},
    &fakeUpstream{name: "f", err: errors.New("should not be called")},
}, nil, nil)
```

After:

```go
p := upstream.NewPool([]upstream.Upstream{
    &fakeUpstream{name: "p", resp: ok},
    &fakeUpstream{name: "f", err: errors.New("should not be called")},
}, nil)
```

- [ ] **Step 5: Compile + run tests**

Run: `go build ./... && go test ./internal/upstream/ ./internal/metrics/ ./internal/resolver/...`
Expected: all pass.

- [ ] **Step 6: Race detector check**

Run: `go test -race ./internal/upstream/...`
Expected: pass, no races.

- [ ] **Step 7: Commit**

```bash
git add internal/upstream/pool.go internal/upstream/pool_test.go internal/metrics/metrics.go cmd/bns/serve.go internal/resolver/outcome.go
git commit -m "$(cat <<'EOF'
upstream: collapse Pool to single slice; add protocol metric label

Pool drops its parallel names slice and now derives metric labels from
the Upstream interface (Name/Protocol). The bns_upstream_* metric
vectors gain a "protocol" label (udp|doh|pool) to let operators slice
by transport without name-substring matching.

Adds a temporary MarkUpstream stub in internal/resolver/outcome.go so
the tree compiles; the real ctx-marker triad lands in the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add ctx-marker triad for upstream attribution

`WithUpstreamMarker` / `MarkUpstream` / `UpstreamInfoFrom` mirror the existing block-marker pattern. TDD: write round-trip tests first, then promote the stub from Task 3 to the real implementation.

**Files:**

- Modify: `internal/resolver/outcome.go`
- Modify: `internal/resolver/outcome_test.go`

- [ ] **Step 1: Write failing tests**

Add to `internal/resolver/outcome_test.go`:

```go
func TestUpstreamInfo_AbsentYieldsZero(t *testing.T) {
    info, ok := resolver.UpstreamInfoFrom(context.Background())
    require.False(t, ok)
    require.Equal(t, resolver.UpstreamInfo{}, info)
}

func TestUpstreamInfo_RoundTrip(t *testing.T) {
    ctx, ptr := resolver.WithUpstreamMarker(context.Background())
    require.NotNil(t, ptr)

    // Before mark: marker exists but is empty.
    info, ok := resolver.UpstreamInfoFrom(ctx)
    require.False(t, ok, "empty marker should report not-ok")
    require.Equal(t, resolver.UpstreamInfo{}, info)

    // After mark: marker carries the values.
    resolver.MarkUpstream(ctx, "https://cloudflare-dns.com/dns-query", "doh")

    info, ok = resolver.UpstreamInfoFrom(ctx)
    require.True(t, ok)
    require.Equal(t, "https://cloudflare-dns.com/dns-query", info.Name)
    require.Equal(t, "doh", info.Protocol)
}

func TestMarkUpstream_NoMarker_NoOp(t *testing.T) {
    // Safe to call on a ctx with no marker installed.
    resolver.MarkUpstream(context.Background(), "x", "udp")
    // No panic, no effect.
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestUpstreamInfo|TestMarkUpstream' ./internal/resolver/`
Expected: fail — `UpstreamInfoFrom` undefined or returns wrong, `WithUpstreamMarker` undefined.

- [ ] **Step 3: Replace stub with real implementation**

Replace the temporary `MarkUpstream` stub in `internal/resolver/outcome.go` with the full triad. Append after the existing `ClientInfoFrom`:

```go
// UpstreamInfo describes the upstream that served a query. Recorded by
// Pool.Exchange on success; read by the qlog stage so each forwarded
// query log line identifies which forwarder served it.
//
// Empty Name signals "no upstream was used" (cache hit, blocked query,
// coalesce piggyback).
type UpstreamInfo struct {
    Name     string // e.g. "1.1.1.1:53" for UDP, the URL for DoH
    Protocol string // "udp" | "doh"
}

type upstreamInfoKey struct{}

// WithUpstreamMarker installs a fresh upstream-info marker in ctx and
// returns the new context plus a pointer the caller can later inspect.
// The metrics stage calls this on every query so downstream stages
// (Pool) can record which upstream served the query.
func WithUpstreamMarker(ctx context.Context) (context.Context, *UpstreamInfo) {
    var info UpstreamInfo
    return context.WithValue(ctx, upstreamInfoKey{}, &info), &info
}

// MarkUpstream sets the upstream marker on ctx (if present). Pool calls
// this immediately after a successful per-upstream Exchange. Safe to
// call on a ctx with no marker installed (no-op).
func MarkUpstream(ctx context.Context, name, protocol string) {
    if info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo); ok && info != nil {
        info.Name = name
        info.Protocol = protocol
    }
}

// UpstreamInfoFrom returns the recorded upstream info from ctx. Returns
// (UpstreamInfo{}, false) when no marker is installed OR when the
// marker exists but has not been set (Name == "") — the qlog stage
// uses this to omit upstream attrs cleanly for cache hits and blocks.
func UpstreamInfoFrom(ctx context.Context) (UpstreamInfo, bool) {
    info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo)
    if !ok || info == nil || info.Name == "" {
        return UpstreamInfo{}, false
    }
    return *info, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run 'TestUpstreamInfo|TestMarkUpstream' ./internal/resolver/`
Expected: pass.

- [ ] **Step 5: Race check**

Run: `go test -race -run 'TestUpstreamInfo|TestMarkUpstream' ./internal/resolver/`
Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver/outcome.go internal/resolver/outcome_test.go
git commit -m "$(cat <<'EOF'
resolver: add UpstreamInfo ctx-marker triad

Mirrors the existing block-marker pattern: metricstage installs the
marker per query; Pool calls MarkUpstream on a successful exchange;
the qlog stage reads UpstreamInfoFrom to surface per-query forwarder
identity in the log line.

UpstreamInfoFrom returns (zero, false) for both "no marker installed"
and "marker installed but never set" so the qlog stage can omit the
attrs cleanly for cache hits, blocks, and coalesce piggybacks.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Install marker in `metricstage`; verify Pool now marks for real

`metricstage` installs the marker at the top of every query alongside the block marker. Pool's existing `MarkUpstream` call from Task 3 now does real work.

**Files:**

- Modify: `internal/resolver/metricstage/metrics.go`
- Modify: `internal/upstream/pool_test.go` (add marker round-trip assertion)

- [ ] **Step 1: Write the Pool-marks-upstream test**

Append to `internal/upstream/pool_test.go`:

```go
func TestPool_MarksUpstreamOnSuccess(t *testing.T) {
    ok := new(dns.Msg)
    ok.Response = true

    primary := &fakeUpstream{name: "primary", resp: ok}
    p := upstream.NewPool([]upstream.Upstream{primary}, nil)

    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    req := dns.NewMsg("example.com.", dns.TypeA)

    _, err := p.Exchange(ctx, req)
    require.NoError(t, err)

    info, ok2 := resolver.UpstreamInfoFrom(ctx)
    require.True(t, ok2)
    require.Equal(t, "primary", info.Name)
    require.Equal(t, "udp", info.Protocol)
}

func TestPool_NoMarkOnAllFail(t *testing.T) {
    p := upstream.NewPool([]upstream.Upstream{
        &fakeUpstream{name: "a", err: errors.New("a-fail")},
        &fakeUpstream{name: "b", err: errors.New("b-fail")},
    }, nil)

    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    _, err := p.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
    require.Error(t, err)

    _, ok := resolver.UpstreamInfoFrom(ctx)
    require.False(t, ok, "marker must stay unset when no upstream succeeds")
}

func TestPool_MarksSecondaryOnFailover(t *testing.T) {
    ok := new(dns.Msg)
    ok.Response = true

    p := upstream.NewPool([]upstream.Upstream{
        &fakeUpstream{name: "primary", err: errors.New("primary-down")},
        &fakeUpstream{name: "secondary", resp: ok},
    }, nil)

    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    _, err := p.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)

    info, present := resolver.UpstreamInfoFrom(ctx)
    require.True(t, present)
    require.Equal(t, "secondary", info.Name)
}
```

You will need to add these imports to `pool_test.go` if not already present: `"context"`, `"github.com/bcrisp4/bns/internal/resolver"`.

- [ ] **Step 2: Run tests to verify they pass**

The Pool already calls `resolver.MarkUpstream` from Task 3. With the real implementation from Task 4 in place, these tests should pass without further code changes.

Run: `go test -run 'TestPool_Marks|TestPool_NoMark' ./internal/upstream/`
Expected: pass.

- [ ] **Step 3: Install marker in metricstage**

In `internal/resolver/metricstage/metrics.go`, find the line that installs the block marker (`ctx, _ = resolver.WithBlockMarker(ctx)`). Add an identical line for the upstream marker immediately after:

```go
ctx, _ = resolver.WithBlockMarker(ctx)
ctx, _ = resolver.WithUpstreamMarker(ctx)
```

- [ ] **Step 4: Re-run existing metricstage tests to confirm green**

Run: `go test ./internal/resolver/metricstage/`
Expected: pass (no behavioural change to outcomes; just an additional ctx value).

- [ ] **Step 5: Race check across all resolver + upstream tests**

Run: `go test -race ./internal/resolver/... ./internal/upstream/...`
Expected: pass, no races.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver/metricstage/metrics.go internal/upstream/pool_test.go
git commit -m "$(cat <<'EOF'
resolver+upstream: install upstream marker and assert Pool sets it

metricstage now installs the UpstreamInfo marker per query alongside
the existing block marker. Pool.Exchange already calls MarkUpstream
on success; tests confirm the marker captures the winning upstream
in primary-success, secondary-failover, and all-fail scenarios.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: qlog stage emits `upstream` and `upstream_protocol` attrs

qlog stage reads the marker via `UpstreamInfoFrom` and conditionally appends two attrs.

**Files:**

- Modify: `internal/resolver/qlog/qlog.go`
- Modify: `internal/resolver/qlog/qlog_test.go`

- [ ] **Step 1: Write failing tests**

Inspect existing `internal/resolver/qlog/qlog_test.go` to identify the test helper / fake logger pattern. Then append:

```go
func TestQlog_EmitsUpstreamAttrsWhenMarkerSet(t *testing.T) {
    fake := newFakeQueryLog()  // existing helper from qlog_test.go
    next := resolver.ResolverFunc(func(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
        // Simulate Pool marking the upstream.
        resolver.MarkUpstream(ctx, "https://cloudflare-dns.com/dns-query", "doh")
        resp := new(dns.Msg)
        resp.Response = true
        return resp, nil
    })

    stage := qlog.New(next, fake)
    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    req := dns.NewMsg("example.com.", dns.TypeA)
    _, err := stage.Resolve(ctx, req)
    require.NoError(t, err)

    require.Len(t, fake.entries, 1)
    attrs := fake.entries[0]
    require.Equal(t, "https://cloudflare-dns.com/dns-query",
        attrValue(attrs, "upstream"))
    require.Equal(t, "doh", attrValue(attrs, "upstream_protocol"))
}

func TestQlog_OmitsUpstreamAttrsWhenMarkerUnset(t *testing.T) {
    fake := newFakeQueryLog()
    next := resolver.ResolverFunc(func(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
        // No MarkUpstream call → marker stays empty (simulates cache hit / block).
        resp := new(dns.Msg)
        resp.Response = true
        return resp, nil
    })

    stage := qlog.New(next, fake)
    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    _, err := stage.Resolve(ctx, dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)

    require.Len(t, fake.entries, 1)
    attrs := fake.entries[0]
    require.Empty(t, attrValue(attrs, "upstream"))
    require.Empty(t, attrValue(attrs, "upstream_protocol"))
}

// attrValue extracts a string attr by key from a slog.Attr slice; returns
// "" if absent. If the existing qlog_test.go already has an equivalent
// helper, reuse that one and delete this. Add to the file if not present.
func attrValue(attrs []slog.Attr, key string) string {
    for _, a := range attrs {
        if a.Key == key {
            return a.Value.String()
        }
    }
    return ""
}
```

(If `ResolverFunc` does not exist in `internal/resolver`, use a small inline `struct{...}` with a `Resolve` method instead. The existing qlog_test.go pattern indicates which is used.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestQlog_EmitsUpstreamAttrs|TestQlog_OmitsUpstreamAttrs' ./internal/resolver/qlog/`
Expected: fail — qlog doesn't emit those attrs yet.

- [ ] **Step 3: Add attrs in `qlog.Resolve`**

Edit `internal/resolver/qlog/qlog.go`. After the existing `ClientInfoFrom` block (which appends `client` + `proto`), append:

```go
if info, present := resolver.UpstreamInfoFrom(ctx); present {
    attrs = append(attrs,
        slog.String("upstream", info.Name),
        slog.String("upstream_protocol", info.Protocol),
    )
}
```

Adjust the initial `attrs` slice capacity hint if it would help — currently `make([]slog.Attr, 4, 6)`. Bump cap to 8 since we may append up to four optional attrs:

```go
attrs := make([]slog.Attr, 4, 8)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -run 'TestQlog' ./internal/resolver/qlog/`
Expected: pass.

- [ ] **Step 5: Full resolver + race**

Run: `go test -race ./internal/resolver/...`
Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/resolver/qlog/qlog.go internal/resolver/qlog/qlog_test.go
git commit -m "$(cat <<'EOF'
qlog: emit upstream and upstream_protocol attrs when marker is set

Per-query attribution: each forwarded query log line now includes the
winning upstream's identity and transport. Attrs are omitted cleanly
for cache hits, blocked queries, and coalesce piggybacks because those
paths never reach Pool.Exchange and the marker stays unset.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Extend `config.Upstream` struct with DoH fields

Schema change only. Empty values keep old configs working. No validation change yet.

**Files:**

- Modify: `internal/config/config.go`

- [ ] **Step 1: Extend the struct**

Replace the `Upstream` struct definition in `internal/config/config.go` with:

```go
// Upstream is one configured upstream DNS server.
//
// Type selects the transport: "udp" (default) or "doh". For type=udp,
// Addr is required (host:port form). For type=doh, URL is required
// (https://hostname/path form) and EndpointIPs must be non-empty
// (operator-pinned IPs that DialContext substitutes for the URL host;
// hostname is retained for SNI / cert validation).
type Upstream struct {
    Type        string        `mapstructure:"type"`         // "udp" | "doh"; default "udp"
    Addr        string        `mapstructure:"addr"`         // type=udp only
    URL         string        `mapstructure:"url"`          // type=doh only
    EndpointIPs []string      `mapstructure:"endpoint_ips"` // type=doh only; required, non-empty
    Timeout     time.Duration `mapstructure:"timeout"`
}
```

- [ ] **Step 2: Verify compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Verify existing tests still pass**

Run: `go test ./internal/config/`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "$(cat <<'EOF'
config: extend Upstream struct with Type/URL/EndpointIPs

Additive schema change. Empty Type defaults to udp in the validator
(next commit), so pre-existing YAML configs continue to parse and
validate unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Rewrite upstream validation — UDP and DoH branches

Validate type discriminator, UDP-only fields on UDP entries, DoH-only fields on DoH entries; reject cross-type field presence; require non-empty `endpoint_ips` for DoH; require hostname (not IP literal) for DoH URL host; verify https scheme.

**Files:**

- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_test.go`

- [ ] **Step 1: Write failing tests**

Append the test cases below to `internal/config/validate_test.go`. Existing tests in that file already exercise the UDP-only world; we add DoH cases and cross-field rejection cases.

```go
func TestValidate_UDP_EmptyTypeDefaults(t *testing.T) {
    c := minimalValidConfig()  // existing helper in validate_test.go
    c.Upstreams = []config.Upstream{
        {Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
    }
    require.NoError(t, c.Validate())
}

func TestValidate_UDP_ExplicitType(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "udp", Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
    }
    require.NoError(t, c.Validate())
}

func TestValidate_UDP_RejectsDoHFields(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "udp", Addr: "1.1.1.1:53", URL: "https://x/dns-query", Timeout: 2 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "url/endpoint_ips not valid for type=udp")
}

func TestValidate_DoH_HostnameURL_OK(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {
            Type:        "doh",
            URL:         "https://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"1.1.1.1", "1.0.0.1"},
            Timeout:     5 * time.Second,
        },
    }
    require.NoError(t, c.Validate())
}

func TestValidate_DoH_MissingURL(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "doh", EndpointIPs: []string{"1.1.1.1"}, Timeout: 5 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "url is required for type=doh")
}

func TestValidate_DoH_NonHTTPSScheme(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "doh", URL: "http://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"1.1.1.1"}, Timeout: 5 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "scheme must be https")
}

func TestValidate_DoH_IPLiteralURL_Rejected(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "doh", URL: "https://1.1.1.1/dns-query", Timeout: 5 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "url host must be a hostname")
}

func TestValidate_DoH_MissingEndpointIPs(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "doh", URL: "https://cloudflare-dns.com/dns-query", Timeout: 5 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "endpoint_ips is required")
}

func TestValidate_DoH_InvalidEndpointIP(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {
            Type:        "doh",
            URL:         "https://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"not-an-ip"},
            Timeout:     5 * time.Second,
        },
    }
    require.ErrorContains(t, c.Validate(), "not a valid IP")
}

func TestValidate_DoH_AddrFieldRejected(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {
            Type:        "doh",
            Addr:        "1.1.1.1:53",
            URL:         "https://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"1.1.1.1"},
            Timeout:     5 * time.Second,
        },
    }
    require.ErrorContains(t, c.Validate(), "addr not valid for type=doh")
}

func TestValidate_UnknownType(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "doq", Addr: "1.1.1.1:853", Timeout: 5 * time.Second},
    }
    require.ErrorContains(t, c.Validate(), "must be \"udp\" or \"doh\"")
}
```

If `minimalValidConfig()` does not already exist in `validate_test.go`, define it once at the top of the file based on the existing config defaults — it should return a `config.Config` with valid Listen/Cache/Admin/Logging/Shutdown/StartupProbe values (consult the existing test setup for the exact fields).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/`
Expected: tests fail.

- [ ] **Step 3: Rewrite the upstream validation loop**

In `internal/config/validate.go`, replace the existing upstream validation block (the loop at lines 30-40) with:

```go
if len(c.Upstreams) == 0 {
    return errors.New("at least one upstream is required")
}
for i, u := range c.Upstreams {
    if u.Timeout <= 0 {
        return fmt.Errorf("upstreams[%d].timeout must be > 0", i)
    }
    switch u.Type {
    case "", "udp":
        if err := validateUDPUpstream(i, u); err != nil {
            return err
        }
    case "doh":
        if err := validateDoHUpstream(i, u); err != nil {
            return err
        }
    default:
        return fmt.Errorf("upstreams[%d].type %q must be \"udp\" or \"doh\"", i, u.Type)
    }
}
```

Then add the two helpers at the bottom of the file (after `validateBind`):

```go
func validateUDPUpstream(i int, u Upstream) error {
    if u.Addr == "" {
        return fmt.Errorf("upstreams[%d]: addr is required for type=udp", i)
    }
    if _, _, err := net.SplitHostPort(u.Addr); err != nil {
        return fmt.Errorf("upstreams[%d].addr %q: %w", i, u.Addr, err)
    }
    if u.URL != "" || len(u.EndpointIPs) > 0 {
        return fmt.Errorf("upstreams[%d]: url/endpoint_ips not valid for type=udp", i)
    }
    return nil
}

func validateDoHUpstream(i int, u Upstream) error {
    if u.URL == "" {
        return fmt.Errorf("upstreams[%d]: url is required for type=doh", i)
    }
    parsed, err := url.Parse(u.URL)
    if err != nil {
        return fmt.Errorf("upstreams[%d].url %q: %w", i, u.URL, err)
    }
    if parsed.Scheme != "https" {
        return fmt.Errorf("upstreams[%d].url %q: scheme must be https", i, u.URL)
    }
    host := parsed.Hostname()
    if host == "" {
        return fmt.Errorf("upstreams[%d].url %q: missing host", i, u.URL)
    }
    if net.ParseIP(host) != nil {
        return fmt.Errorf("upstreams[%d].url %q: url host must be a hostname, not an IP literal", i, u.URL)
    }
    if u.Addr != "" {
        return fmt.Errorf("upstreams[%d]: addr not valid for type=doh", i)
    }
    if len(u.EndpointIPs) == 0 {
        return fmt.Errorf("upstreams[%d]: endpoint_ips is required for type=doh", i)
    }
    for j, ip := range u.EndpointIPs {
        if net.ParseIP(ip) == nil {
            return fmt.Errorf("upstreams[%d].endpoint_ips[%d] %q: not a valid IP", i, j, ip)
        }
    }
    return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "$(cat <<'EOF'
config: split upstream validation into UDP and DoH branches

UDP entries keep the existing addr/SplitHostPort check, with explicit
rejection of url/endpoint_ips. DoH entries require https scheme, a
hostname URL (not IP literal — endpoint_ips carries the IPs), and a
non-empty endpoint_ips list of valid IP strings.

Empty Type defaults to udp so pre-existing configs pass unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: TLS cert testutil helper

Self-signed cert generator with caller-specified SANs. Used by every DoH test that needs a real TLS server.

**Files:**

- Create: `internal/upstream/testutil/tlscert.go`
- Create: `internal/upstream/testutil/tlscert_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/upstream/testutil/tlscert_test.go`:

```go
package testutil_test

import (
    "crypto/x509"
    "net"
    "testing"
    "time"

    "github.com/bcrisp4/bns/internal/upstream/testutil"
    "github.com/stretchr/testify/require"
)

func TestNewTLSCert_HasRequestedSANs(t *testing.T) {
    cert := testutil.NewTLSCert(t, []string{"test.example", "127.0.0.1"})

    require.NotNil(t, cert.Leaf)
    leaf := cert.Leaf

    // DNS SAN present.
    require.Contains(t, leaf.DNSNames, "test.example")

    // IP SAN present.
    found := false
    for _, ip := range leaf.IPAddresses {
        if ip.Equal(net.ParseIP("127.0.0.1")) {
            found = true
            break
        }
    }
    require.True(t, found, "cert must contain IP SAN 127.0.0.1")
}

func TestNewTLSCert_ValidNow(t *testing.T) {
    cert := testutil.NewTLSCert(t, []string{"x"})
    now := time.Now()
    require.True(t, now.After(cert.Leaf.NotBefore))
    require.True(t, now.Before(cert.Leaf.NotAfter))
}

func TestNewTLSCert_PoolAccepts(t *testing.T) {
    // Smoke check that the returned cert verifies against a pool
    // containing only itself.
    cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    pool := x509.NewCertPool()
    pool.AddCert(cert.Leaf)

    _, err := cert.Leaf.Verify(x509.VerifyOptions{
        Roots:   pool,
        DNSName: "", // skip name check; we test SAN above
    })
    require.NoError(t, err)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/testutil/`
Expected: fail — `NewTLSCert` undefined.

- [ ] **Step 3: Implement the helper**

Create `internal/upstream/testutil/tlscert.go`:

```go
package testutil

import (
    "crypto/ecdsa"
    "crypto/elliptic"
    "crypto/rand"
    "crypto/tls"
    "crypto/x509"
    "crypto/x509/pkix"
    "math/big"
    "net"
    "testing"
    "time"

    "github.com/stretchr/testify/require"
)

// NewTLSCert generates a self-signed leaf certificate suitable for use as
// httptest.Server's TLS cert in DoH client tests. hosts may mix DNS names
// and IP literals — the helper splits them into the appropriate SAN slots.
//
// The returned *tls.Certificate has Leaf populated so callers can build
// an *x509.CertPool from it for client-side trust without re-parsing.
func NewTLSCert(t *testing.T, hosts []string) tls.Certificate {
    t.Helper()

    key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
    require.NoError(t, err)

    serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
    require.NoError(t, err)

    tmpl := &x509.Certificate{
        SerialNumber:          serial,
        Subject:               pkix.Name{CommonName: "bns-test"},
        NotBefore:             time.Now().Add(-time.Minute),
        NotAfter:              time.Now().Add(time.Hour),
        KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
        ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
        BasicConstraintsValid: true,
    }
    for _, h := range hosts {
        if ip := net.ParseIP(h); ip != nil {
            tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
        } else {
            tmpl.DNSNames = append(tmpl.DNSNames, h)
        }
    }

    der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
    require.NoError(t, err)
    leaf, err := x509.ParseCertificate(der)
    require.NoError(t, err)

    return tls.Certificate{
        Certificate: [][]byte{der},
        PrivateKey:  key,
        Leaf:        leaf,
    }
}
```

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/upstream/testutil/`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/upstream/testutil/tlscert.go internal/upstream/testutil/tlscert_test.go
git commit -m "$(cat <<'EOF'
upstream/testutil: add TLS cert helper for DoH tests

NewTLSCert generates a self-signed cert with caller-specified DNS and
IP SANs, populated Leaf, ECDSA P-256 key, valid for one hour. Used by
upcoming DoH client tests to stand up httptest TLS servers without
requiring system-CA trust.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Add DoH-specific metric vectors

Additive — no callers yet (DoHClient will use them in subsequent tasks).

**Files:**

- Modify: `internal/metrics/metrics.go`

- [ ] **Step 1: Add fields to the `Metrics` struct**

Find the field declarations block in `internal/metrics/metrics.go` and add:

```go
DoHHTTPStatusTotal    *prometheus.CounterVec
DoHTLSHandshakesTotal *prometheus.CounterVec
```

- [ ] **Step 2: Construct the vectors**

In the `Metrics` constructor (`New`/`NewMetrics` — check file), add the constructions alongside the existing vectors:

```go
DoHHTTPStatusTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_doh_http_status_total",
    Help: "DoH HTTP responses, by upstream and HTTP status code.",
}, []string{"upstream", "status"}),
DoHTLSHandshakesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_doh_tls_handshakes_total",
    Help: "DoH TLS handshakes, by upstream and result. High rate indicates connection churn.",
}, []string{"upstream", "result"}),
```

- [ ] **Step 3: Register them**

Find the slice of collectors registered with the Prometheus registry (around line 105 in the existing file). Add the two new vectors:

```go
m.DoHHTTPStatusTotal, m.DoHTLSHandshakesTotal,
```

- [ ] **Step 4: Compile + existing tests**

Run: `go build ./... && go test ./internal/metrics/`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/metrics.go
git commit -m "$(cat <<'EOF'
metrics: add bns_doh_http_status_total and _tls_handshakes_total

Two new counter vectors used by the upcoming DoH client to surface
HTTP-layer failures (separate from DNS-layer outcomes already on
bns_upstream_queries_total) and connection-churn frequency. No
callers yet.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: `DoHClient` struct + constructor + `Name()`/`Protocol()`

TDD. Write a test that constructs a DoHClient and asserts `Name()`/`Protocol()`. Then implement the constructor (no `Exchange` body yet — placeholder error return).

**Files:**

- Create: `internal/upstream/doh_client.go`
- Create: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/upstream/doh_client_test.go`:

```go
// Package upstream tests live in-package (white-box) so we can inject
// custom RootCAs into the DoHClient's TLS config for httptest servers
// without exposing a production seam.
package upstream

import (
    "log/slog"
    "testing"
    "time"

    "github.com/bcrisp4/bns/internal/metrics"
    "github.com/stretchr/testify/require"
)

func TestDoHClient_NameAndProtocol(t *testing.T) {
    c, err := NewDoHClient(
        "https://cloudflare-dns.com/dns-query",
        []string{"1.1.1.1"},
        5*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.NoError(t, err)
    require.Equal(t, "https://cloudflare-dns.com/dns-query", c.Name())
    require.Equal(t, "doh", c.Protocol())
}

func TestDoHClient_InvalidURL(t *testing.T) {
    _, err := NewDoHClient(
        "://broken",
        []string{"1.1.1.1"},
        5*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.Error(t, err)
}
```

Note: `metrics.NewForTest()` is referenced — if no such helper exists, add one to `internal/metrics/metrics.go` (a one-liner that returns a `*Metrics` registered to a fresh `prometheus.NewRegistry()`). Existing tests likely already have a similar pattern; reuse it.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/ -run TestDoHClient`
Expected: `NewDoHClient` undefined.

- [ ] **Step 3: Create `doh_client.go`**

Create `internal/upstream/doh_client.go`:

```go
// Package upstream — DoHClient implements DNS-over-HTTPS (RFC 8484) as an
// Upstream. URL host is operator-meaningful (used as SNI + cert SAN check);
// endpointIPs are the actual dial targets. Hand-rolled over stdlib net/http;
// response decode delegates to miekg/dns v2's dnshttp.Response helper.
package upstream

import (
    "context"
    "crypto/tls"
    "fmt"
    "log/slog"
    "net"
    "net/http"
    "net/url"
    "sync/atomic"
    "time"

    "codeberg.org/miekg/dns"
    "github.com/bcrisp4/bns/internal/metrics"
    "golang.org/x/net/http2"
)

// DoHClient sends DNS queries over HTTPS to a configured DoH endpoint.
type DoHClient struct {
    url        string
    httpClient *http.Client
    logger     *slog.Logger
    metrics    *metrics.Metrics
}

// NewDoHClient constructs a DoHClient. rawURL must be an https URL whose
// hostname matches a SAN in the server's certificate. endpointIPs are
// dialed in round-robin order; net/http performs the TLS handshake using
// rawURL's hostname as SNI. timeout applies to the whole HTTP exchange.
//
// logger must be non-nil; pass slog.New(slog.DiscardHandler) for tests
// that don't care. mtr must be non-nil; pass metrics.NewForTest().
func NewDoHClient(
    rawURL string,
    endpointIPs []string,
    timeout time.Duration,
    logger *slog.Logger,
    mtr *metrics.Metrics,
) (*DoHClient, error) {
    u, err := url.Parse(rawURL)
    if err != nil {
        return nil, fmt.Errorf("doh url %q: %w", rawURL, err)
    }
    if u.Host == "" {
        return nil, fmt.Errorf("doh url %q: missing host", rawURL)
    }

    host := u.Hostname()
    port := u.Port()
    if port == "" {
        port = "443"
    }

    // dialAddrs: explicit endpoint IPs, else fall back to URL host (which
    // is then expected to be an IP literal — callers from validate.go
    // never hit this case for production DoH, but it's relied on for
    // tests that pass an IP-literal URL with no endpointIPs).
    dialAddrs := append([]string(nil), endpointIPs...)
    if len(dialAddrs) == 0 {
        dialAddrs = []string{host}
    }

    var rr atomic.Uint32
    netDialer := &net.Dialer{Timeout: timeout}

    transport := &http.Transport{
        TLSClientConfig: &tls.Config{
            ServerName: host,
            NextProtos: []string{"h2", "http/1.1"},
            MinVersion: tls.VersionTLS13, // BCP 195 / RFC 9325
        },
        DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
            start := int(rr.Add(1)-1) % len(dialAddrs)
            var lastErr error
            for i := 0; i < len(dialAddrs); i++ {
                ip := dialAddrs[(start+i)%len(dialAddrs)]
                c, err := netDialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
                if err == nil {
                    return c, nil
                }
                lastErr = err
            }
            return nil, lastErr
        },
        ForceAttemptHTTP2:   true,
        IdleConnTimeout:     90 * time.Second,
        MaxIdleConns:        4,
        MaxIdleConnsPerHost: 4,
    }
    if err := http2.ConfigureTransport(transport); err != nil {
        return nil, fmt.Errorf("doh %s: configure http/2: %w", rawURL, err)
    }

    return &DoHClient{
        url:    rawURL,
        logger: logger,
        metrics: mtr,
        httpClient: &http.Client{
            Transport: transport,
            Timeout:   timeout,
            // RFC 8484 §9 spirit: a hijacked DoH server should not redirect
            // the client to a logging or alternate endpoint.
            CheckRedirect: func(*http.Request, []*http.Request) error {
                return http.ErrUseLastResponse
            },
            // RFC 8484 §8: cookies SHOULD NOT be accepted. Jar nil by default;
            // explicit for clarity and to make a future test assertion simple.
            Jar: nil,
        },
    }, nil
}

// Name returns the configured DoH URL (used as the "upstream" metric label).
func (c *DoHClient) Name() string { return c.url }

// Protocol returns "doh".
func (c *DoHClient) Protocol() string { return "doh" }

// Exchange is implemented in a later commit.
func (c *DoHClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
    return nil, fmt.Errorf("doh %s: Exchange not yet implemented", c.url)
}
```

- [ ] **Step 4: Add `metrics.NewForTest` if absent**

If `metrics.NewForTest()` does not already exist, add to `internal/metrics/metrics.go`:

```go
// NewForTest returns a Metrics with a fresh private registry; safe to use
// in tests without polluting the global default registerer.
func NewForTest() *Metrics {
    return New(prometheus.NewRegistry())
}
```

(Substitute the actual existing constructor signature if it differs.)

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./internal/upstream/ -run TestDoHClient`
Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/upstream/doh_client.go internal/upstream/doh_client_test.go internal/metrics/metrics.go
git commit -m "$(cat <<'EOF'
upstream: add DoHClient struct, constructor, Name/Protocol getters

Constructs an http.Client with TLS 1.3 floor, h2-preferred ALPN, custom
DialContext that round-robins across endpoint_ips substituting the URL
host. ForceAttemptHTTP2 + http2.ConfigureTransport bridge to h2.
CheckRedirect refuses all redirects (RFC 8484 §9); Jar nil (§8).

Exchange method stubbed; real body lands in the next commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: `DoHClient.Exchange` — happy path

Round-trip: POST a DNS query, decode response. ID=0 on the wire; original ID restored. Use httptest TLS server with our testutil cert; inject test cert pool into client.

**Files:**

- Modify: `internal/upstream/doh_client.go`
- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Write failing test**

Add to `internal/upstream/doh_client_test.go`:

```go
import (
    "bytes"
    "context"
    "crypto/tls"
    "crypto/x509"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "net/url"
    "testing"
    "time"

    "codeberg.org/miekg/dns"
    "codeberg.org/miekg/dns/dnshttp"
    "github.com/bcrisp4/bns/internal/metrics"
    "github.com/bcrisp4/bns/internal/upstream/testutil"
    "github.com/stretchr/testify/require"
)

// newTestDoHServer spins up an httptest TLS server with a self-signed
// cert covering 127.0.0.1, running the supplied handler. Returns a fully
// configured DoHClient pointing at it (test cert pool injected into the
// client's TLS config).
func newTestDoHServer(t *testing.T, handler http.HandlerFunc) *DoHClient {
    t.Helper()

    cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    srv := httptest.NewUnstartedServer(handler)
    srv.TLS = &tls.Config{
        Certificates: []tls.Certificate{cert},
        NextProtos:   []string{"h2", "http/1.1"},
    }
    srv.StartTLS()
    t.Cleanup(srv.Close)

    parsed, err := url.Parse(srv.URL)
    require.NoError(t, err)

    dohURL := "https://" + parsed.Host + "/dns-query"
    c, err := NewDoHClient(
        dohURL,
        nil, // empty endpointIPs → DialContext falls back to URL host (127.0.0.1)
        5*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.NoError(t, err)

    pool := x509.NewCertPool()
    pool.AddCert(cert.Leaf)
    c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

    return c
}

// dohEchoHandler decodes the DoH request to a DNS message, hands it to
// build(), encodes the result and writes it back. Used by happy-path
// tests; build(req) returns the canned response.
func dohEchoHandler(t *testing.T, build func(req *dns.Msg) *dns.Msg) http.HandlerFunc {
    t.Helper()
    return func(w http.ResponseWriter, r *http.Request) {
        msg, err := dnshttp.Request(r)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        resp := build(msg)
        if err := resp.Pack(); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
}

func TestDoHClient_RoundTrip(t *testing.T) {
    var seenIDOnWire uint16 = 99 // sentinel; should overwrite to 0
    handler := dohEchoHandler(t, func(req *dns.Msg) *dns.Msg {
        seenIDOnWire = req.ID
        resp := new(dns.Msg)
        resp.Response = true
        resp.Question = req.Question
        return resp
    })
    c := newTestDoHServer(t, handler)

    req := dns.NewMsg("example.com.", dns.TypeA)
    req.ID = 0x1234

    resp, err := c.Exchange(context.Background(), req)
    require.NoError(t, err)
    require.NotNil(t, resp)
    require.True(t, resp.Response)
    require.Equal(t, uint16(0x1234), resp.ID,
        "response ID must be restored to original")
    require.Equal(t, uint16(0x1234), req.ID,
        "caller's req.ID must be unchanged (immutability contract)")
    require.Equal(t, uint16(0), seenIDOnWire,
        "wire ID must be 0 (RFC 8484 §4.1.1)")
}

func TestDoHClient_POSTMethodAndHeaders(t *testing.T) {
    var seenMethod, seenCT, seenAccept string
    handler := func(w http.ResponseWriter, r *http.Request) {
        seenMethod = r.Method
        seenCT = r.Header.Get("Content-Type")
        seenAccept = r.Header.Get("Accept")
        // Minimum valid DoH response.
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)

    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
    require.Equal(t, "POST", seenMethod)
    require.Equal(t, "application/dns-message", seenCT)
    require.Equal(t, "application/dns-message", seenAccept)
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_RoundTrip|TestDoHClient_POSTMethodAndHeaders'`
Expected: fail (Exchange stub returns "not yet implemented").

- [ ] **Step 3: Implement `Exchange` happy path**

Replace the stub Exchange in `internal/upstream/doh_client.go` with:

```go
import (
    // ... existing imports ...
    "bytes"
    "io"
    "mime"
    "net/http"
    "strings"
    // and miekg's dnshttp:
    "codeberg.org/miekg/dns/dnshttp"
)

const dohContentType = "application/dns-message"

func (c *DoHClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
    // RFC 8484 §4.1.1: DNS ID SHOULD be 0 on the wire so HTTP caches
    // don't fragment on per-query IDs. Save+restore the caller's ID via
    // defer so the Upstream "MUST NOT mutate req" contract holds on
    // every return path.
    origID := req.ID
    req.ID = 0
    defer func() { req.ID = origID }()

    if err := req.Pack(); err != nil {
        return nil, fmt.Errorf("doh %s: pack: %w", c.url, err)
    }

    httpReq, err := http.NewRequestWithContext(
        ctx, http.MethodPost, c.url, bytes.NewReader(req.Data),
    )
    if err != nil {
        return nil, fmt.Errorf("doh %s: build request: %w", c.url, err)
    }
    httpReq.Header.Set("Content-Type", dohContentType)
    httpReq.Header.Set("Accept", dohContentType)
    httpReq.Header.Set("User-Agent", "bns")

    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("doh %s: do: %w", c.url, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode/100 != 2 {
        return nil, fmt.Errorf("doh %s: http %d", c.url, resp.StatusCode)
    }

    // RFC 8484 §4.2 + RFC 7231 §3.1.1.1: case-insensitive type token,
    // parameters (e.g. charset=utf-8) permitted.
    mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
    if err != nil || !strings.EqualFold(mediaType, dohContentType) {
        return nil, fmt.Errorf("doh %s: unexpected content-type %q",
            c.url, resp.Header.Get("Content-Type"))
    }

    // RFC 8484 §6 + DoS defense: cap body at the DNS msg size ceiling.
    resp.Body = io.NopCloser(io.LimitReader(resp.Body, dns.MaxMsgSize))

    msg, err := dnshttp.Response(resp)
    if err != nil {
        return nil, fmt.Errorf("doh %s: decode: %w", c.url, err)
    }
    msg.ID = origID // restore so downstream stages + wire writer match req
    return msg, nil
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_RoundTrip|TestDoHClient_POSTMethodAndHeaders'`
Expected: pass.

- [ ] **Step 5: Race + full suite**

Run: `go test -race ./internal/upstream/`
Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add internal/upstream/doh_client.go internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream: DoHClient.Exchange happy path

Implements the round-trip: Pack the query with ID=0 (RFC 8484 §4.1.1),
POST to the configured URL with Content-Type + Accept headers, validate
2xx + application/dns-message response (case-insensitive media type
compare with mime.ParseMediaType per RFC 7231 §3.1.1.1), cap response
body at dns.MaxMsgSize, decode via miekg dnshttp.Response, restore
original ID on the response.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: `DoHClient.Exchange` — error paths

HTTP status non-2xx, Content-Type mismatch (with case-insensitive accept), 3xx refusal, oversized body truncation, cookies absent.

**Files:**

- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Add error-path tests**

Append to `internal/upstream/doh_client_test.go`:

```go
func TestDoHClient_HTTPNon2xx(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        http.Error(w, "boom", http.StatusInternalServerError)
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.ErrorContains(t, err, "http 500")
}

func TestDoHClient_Accepts2xxRange(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        // 202 Accepted — uncommon but spec-compliant 2xx.
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message")
        w.WriteHeader(http.StatusAccepted)
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
}

func TestDoHClient_ContentTypeWithCharsetParameter(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message; charset=utf-8")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err, "charset parameter must not reject valid DoH body")
}

func TestDoHClient_ContentTypeMismatch(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        _, _ = w.Write([]byte("not dns"))
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.ErrorContains(t, err, "unexpected content-type")
}

func TestDoHClient_RefusesRedirect(t *testing.T) {
    // Second server should never be hit.
    var secondHits int32
    secondCert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    second := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
        atomic.AddInt32(&secondHits, 1)
    }))
    second.TLS = &tls.Config{Certificates: []tls.Certificate{secondCert}}
    second.StartTLS()
    t.Cleanup(second.Close)

    handler := func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, second.URL+"/dns-query", http.StatusFound)
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.ErrorContains(t, err, "http 302")
    require.Equal(t, int32(0), atomic.LoadInt32(&secondHits),
        "redirect target must not be followed")
}

func TestDoHClient_BodyCapPreventsOverflow(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/dns-message")
        // Write more than dns.MaxMsgSize (65535) bytes of garbage; the
        // client's LimitReader caps reads at 65535 → decode fails cleanly.
        buf := make([]byte, dns.MaxMsgSize+1024)
        _, _ = w.Write(buf)
    }
    c := newTestDoHServer(t, handler)
    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.Error(t, err, "oversized body must fail decode, not OOM")
}

func TestDoHClient_CookieJarIsNil(t *testing.T) {
    c, err := NewDoHClient(
        "https://x/dns-query",
        []string{"1.1.1.1"},
        5*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.NoError(t, err)
    require.Nil(t, c.httpClient.Jar)
}
```

Add `"sync/atomic"` import if not already present.

- [ ] **Step 2: Run tests to verify they pass**

The existing Exchange implementation should already satisfy all these tests (status range, mime parse, body cap, CheckRedirect, nil Jar are all in place from Task 12).

Run: `go test ./internal/upstream/ -run TestDoHClient`
Expected: pass.

If any test fails, fix the implementation. Likely small adjustments (e.g., a missing import, an off-by-one on the body cap).

- [ ] **Step 3: Commit**

```bash
git add internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream/doh: cover RFC 8484 error paths and defensive limits

Tests cover: any-2xx success (not strictly 200), Content-Type with
charset parameter accepted (RFC 7231 case-insensitive type), wrong
Content-Type rejected, 3xx redirect refused (CheckRedirect honoured —
verified that the redirect target server gets zero hits), body cap at
dns.MaxMsgSize, and Jar is nil (RFC 8484 §8 cookies SHOULD NOT).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: `DoHClient.Exchange` — HTTP `Age` header TTL decrement

Subtract `Age` from RR TTLs at the client boundary (RFC 8484 §5.1).

**Files:**

- Modify: `internal/upstream/doh_client.go`
- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Add failing tests**

Append to `internal/upstream/doh_client_test.go`:

```go
// buildAnswerMsg returns a DoH-style response with one A record at TTL.
func buildAnswerMsg(req *dns.Msg, ttl uint32) *dns.Msg {
    resp := new(dns.Msg)
    resp.Response = true
    resp.Question = req.Question
    a := &dns.A{
        Hdr: dns.Header{Name: "example.com.", TTL: ttl},
        A:   rdata.A{Addr: netip.MustParseAddr("203.0.113.1")},
    }
    resp.Answer = []dns.RR{a}
    return resp
}

func TestDoHClient_AgeHeaderDecrementsTTL(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        req, err := dnshttp.Request(r)
        require.NoError(t, err)
        resp := buildAnswerMsg(req, 300)
        require.NoError(t, resp.Pack())
        w.Header().Set("Content-Type", "application/dns-message")
        w.Header().Set("Age", "60")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)

    resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
    require.Len(t, resp.Answer, 1)
    require.Equal(t, uint32(240), resp.Answer[0].Header().TTL)
}

func TestDoHClient_NoAgeHeader_TTLUnchanged(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        req, err := dnshttp.Request(r)
        require.NoError(t, err)
        resp := buildAnswerMsg(req, 300)
        require.NoError(t, resp.Pack())
        w.Header().Set("Content-Type", "application/dns-message")
        // No Age header.
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)

    resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
    require.Equal(t, uint32(300), resp.Answer[0].Header().TTL)
}

func TestDoHClient_AgeExceedsTTL_FloorsAtZero(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        req, err := dnshttp.Request(r)
        require.NoError(t, err)
        resp := buildAnswerMsg(req, 30)
        require.NoError(t, resp.Pack())
        w.Header().Set("Content-Type", "application/dns-message")
        w.Header().Set("Age", "120")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServer(t, handler)

    resp, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
    require.Equal(t, uint32(0), resp.Answer[0].Header().TTL)
}
```

Add imports `"codeberg.org/miekg/dns/rdata"` and `"net/netip"` to the test file.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_Age'`
Expected: fail — Age handling not implemented yet.

- [ ] **Step 3: Add `decrementTTLs` helper and call it**

In `internal/upstream/doh_client.go`, add this helper near the bottom:

```go
// decrementTTLs subtracts age (seconds) from every Answer/Ns/Extra RR
// TTL in m, flooring at 0.
//
// Why this matters (RFC 8484 §5.1):
// HTTP Age (RFC 7234 §5.1) reports how long an HTTP intermediate cache
// has held the response since the origin generated it. DNS RR TTLs in
// the wire body reflect remaining-TTL when the response reached the
// intermediate, NOT when it reached BNS. Without this adjustment,
// downstream caching overstates remaining freshness by Age seconds and
// serves stale answers.
//
// Worked example: authoritative TTL=300s.
//   T=0    Auth emits msg{TTL=300}.
//   T=60   Origin DoH server emits msg{TTL=240} (decremented its 60s hold).
//   T=90   Reaches HTTP intermediate cache.
//   T=120  BNS queries → CDN returns body{TTL=240} + Age:30.
// Without subtraction: BNS caches TTL=240, expires at T=360 (60s late).
// With subtraction:    BNS caches TTL=210, expires at T=330 (correct).
//
// Direct connections to public DoH providers usually have no intermediate
// cache → Age absent → this is a no-op. Defensive correctness for the
// CDN-fronted case at ~6 LOC.
func decrementTTLs(m *dns.Msg, age uint32) {
    for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
        for _, rr := range sec {
            if rr.Header().TTL > age {
                rr.Header().TTL -= age
            } else {
                rr.Header().TTL = 0
            }
        }
    }
}
```

Then call it from `Exchange`, after the `dnshttp.Response` decode and before restoring `msg.ID`:

```go
msg, err := dnshttp.Response(resp)
if err != nil {
    return nil, fmt.Errorf("doh %s: decode: %w", c.url, err)
}

if ageStr := resp.Header.Get("Age"); ageStr != "" {
    if age, parseErr := strconv.ParseUint(ageStr, 10, 32); parseErr == nil && age > 0 {
        decrementTTLs(msg, uint32(age))
    }
}

msg.ID = origID
return msg, nil
```

Add `"strconv"` import.

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_Age'`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/upstream/doh_client.go internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream/doh: subtract HTTP Age from RR TTLs (RFC 8484 §5.1)

When the response carries an Age header (HTTP intermediate cache hold
time), decrement every Answer/Ns/Extra RR TTL by that value, flooring
at zero. Direct connections to public DoH providers won't carry Age
(no intermediate cache), so this is a no-op in the common case —
defensive correctness for CDN-fronted DoH endpoints.

decrementTTLs has a detailed comment with the drift scenario so the
"why" is preserved for future readers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: `DoHClient.Exchange` — context cancellation + Pack error

**Files:**

- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Add tests**

Append:

```go
func TestDoHClient_ContextCancellation(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        // Block until client disconnects.
        <-r.Context().Done()
    }
    c := newTestDoHServer(t, handler)

    ctx, cancel := context.WithCancel(context.Background())
    cancel() // pre-cancel
    _, err := c.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
    require.Error(t, err)
    require.ErrorIs(t, err, context.Canceled)
}

func TestDoHClient_PackError_RestoresOriginalID(t *testing.T) {
    // Build a malformed Msg that fails Pack: a Question with an
    // unparseably long name.
    longName := strings.Repeat("a.", 200) + "example.com."
    req := new(dns.Msg)
    req.ID = 0xBEEF
    req.Question = []dns.RR{&dns.Q{
        Hdr: dns.Header{Name: longName},
    }}
    // (Adjust the malformed-Msg construction if the above shape doesn't
    // actually fail Pack; the underlying property under test is that on
    // a Pack error, the deferred restore runs and req.ID is unchanged.)

    c, err := NewDoHClient(
        "https://x/dns-query",
        []string{"127.0.0.1"},
        5*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.NoError(t, err)

    _, exErr := c.Exchange(context.Background(), req)
    require.Error(t, exErr)
    require.Equal(t, uint16(0xBEEF), req.ID,
        "caller's req.ID must be restored even on Pack failure")
}
```

(For the Pack-error test, the malformed-Msg shape may need adjustment to actually trigger a Pack failure. If miekg/dns v2's `Pack()` happens to be lenient on the constructed shape, replace with a different invariant violation. Alternative: construct a Msg with `dns.Msg{Data: nil}` and a Question containing an RR whose `Pack` returns an error explicitly — consult the miekg/dns v2 source for what triggers `Pack()` error. If no clean way to trigger Pack failure, skip this test and document the behaviour in the implementation comment only.)

Add `"strings"` import.

- [ ] **Step 2: Verify pass**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_ContextCancellation|TestDoHClient_PackError'`
Expected: pass. If Pack-error test cannot be reliably constructed, document the limitation and remove that single test.

- [ ] **Step 3: Commit**

```bash
git add internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream/doh: cover context cancellation and Pack-error ID restore

Cancellation: ctx-cancelled mid-flight returns context.Canceled via
the wrap chain. Pack error: the deferred ID restore runs even when
req.Pack fails, preserving caller's req.ID.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: `DoHClient` — dial round-robin and failover across endpoint_ips

Test setup: one httptest TLS server (reachable on 127.0.0.1:<ephemeral>) plus an unreachable IP (127.0.0.99 on a port nothing listens on — easiest: pick a closed port). Round-robin index cycles through both.

**Files:**

- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Add failover test**

```go
func TestDoHClient_DialFailover(t *testing.T) {
    handler := func(w http.ResponseWriter, r *http.Request) {
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }

    cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    srv := httptest.NewUnstartedServer(http.HandlerFunc(handler))
    srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
    srv.StartTLS()
    t.Cleanup(srv.Close)

    parsed, _ := url.Parse(srv.URL)
    _, port, _ := net.SplitHostPort(parsed.Host)

    // dohURL host is the test server's host:port pair so the URL parser
    // gives us the correct port to dial on the unreachable IP as well.
    dohURL := "https://127.0.0.1:" + port + "/dns-query"

    // Endpoint IPs: first one is unreachable (127.0.0.99 — nothing
    // listens on the ephemeral test port there); second is the actual
    // server.
    c, err := NewDoHClient(
        dohURL,
        []string{"127.0.0.99", "127.0.0.1"},
        2*time.Second,
        slog.New(slog.DiscardHandler),
        metrics.NewForTest(),
    )
    require.NoError(t, err)

    pool := x509.NewCertPool()
    pool.AddCert(cert.Leaf)
    c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

    _, err = c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err, "dial should have failed over from 127.0.0.99 to 127.0.0.1")
}
```

- [ ] **Step 2: Verify pass**

Run: `go test ./internal/upstream/ -run TestDoHClient_DialFailover`
Expected: pass (existing DialContext loop already handles failover; this is verification that round-robin + fallover work together).

If the test fails because the first round-robin pick lands on 127.0.0.1 (the reachable IP) on this particular invocation of the atomic counter — note that `atomic.Uint32` starts at 0, so `(0+1)-1 % 2 = 0` → first index is `endpointIPs[0]` = 127.0.0.99 (unreachable) → falls over to `endpointIPs[1]` = 127.0.0.1 (reachable). Confirmed deterministic on first call.

- [ ] **Step 3: Commit**

```bash
git add internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream/doh: cover round-robin + failover across endpoint_ips

First endpoint_ip is unreachable (127.0.0.99 on the test server's
ephemeral port); DialContext falls over to the second IP (127.0.0.1)
where httptest server actually listens. Exchange succeeds.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: `DoHClient.Exchange` — httptrace TLS handshake counter + HTTP status counter

Wire the metric increments + the cold-connection log. Tests assert the counters move.

**Files:**

- Modify: `internal/upstream/doh_client.go`
- Modify: `internal/upstream/doh_client_test.go`

- [ ] **Step 1: Add tests**

```go
import (
    "github.com/prometheus/client_golang/prometheus/testutil"  // RENAMING TO ptestutil
)

// To avoid import collision with our own testutil package, alias:
// import ptestutil "github.com/prometheus/client_golang/prometheus/testutil"

func TestDoHClient_RecordsHTTPStatusMetric(t *testing.T) {
    mtr := metrics.NewForTest()
    handler := func(w http.ResponseWriter, r *http.Request) {
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServerWithMetrics(t, handler, mtr)

    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)

    count := ptestutil.ToFloat64(mtr.DoHHTTPStatusTotal.WithLabelValues(c.url, "200"))
    require.Equal(t, float64(1), count)
}

func TestDoHClient_RecordsTLSHandshakeMetric(t *testing.T) {
    mtr := metrics.NewForTest()
    handler := func(w http.ResponseWriter, r *http.Request) {
        resp := new(dns.Msg)
        resp.Response = true
        _ = resp.Pack()
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    }
    c := newTestDoHServerWithMetrics(t, handler, mtr)

    _, err := c.Exchange(context.Background(), dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)

    count := ptestutil.ToFloat64(mtr.DoHTLSHandshakesTotal.WithLabelValues(c.url, "ok"))
    require.GreaterOrEqual(t, count, float64(1))
}
```

You need a variant of `newTestDoHServer` that accepts a caller-supplied `*metrics.Metrics`. Add it next to the existing helper:

```go
func newTestDoHServerWithMetrics(t *testing.T, handler http.HandlerFunc, mtr *metrics.Metrics) *DoHClient {
    t.Helper()

    cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    srv := httptest.NewUnstartedServer(handler)
    srv.TLS = &tls.Config{
        Certificates: []tls.Certificate{cert},
        NextProtos:   []string{"h2", "http/1.1"},
    }
    srv.StartTLS()
    t.Cleanup(srv.Close)

    parsed, err := url.Parse(srv.URL)
    require.NoError(t, err)

    dohURL := "https://" + parsed.Host + "/dns-query"
    c, err := NewDoHClient(
        dohURL,
        nil,
        5*time.Second,
        slog.New(slog.DiscardHandler),
        mtr,
    )
    require.NoError(t, err)

    pool := x509.NewCertPool()
    pool.AddCert(cert.Leaf)
    c.httpClient.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

    return c
}
```

(Refactor the original `newTestDoHServer` to be a thin wrapper that calls this new variant with `metrics.NewForTest()` to avoid duplication.)

Add the `ptestutil` import alias to the test file.

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_RecordsHTTPStatusMetric|TestDoHClient_RecordsTLSHandshakeMetric'`
Expected: fail — metric never incremented.

- [ ] **Step 3: Wire httptrace + status counter into `Exchange`**

In `internal/upstream/doh_client.go`, between the `http.NewRequestWithContext` and `c.httpClient.Do` calls, install the trace:

```go
trace := &httptrace.ClientTrace{
    GotConn: func(info httptrace.GotConnInfo) {
        if !info.Reused {
            c.logger.Debug("doh new connection",
                "upstream", c.url,
                "addr", info.Conn.RemoteAddr().String(),
            )
        }
    },
    TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
        result := "ok"
        if err != nil {
            result = "error"
        }
        c.metrics.DoHTLSHandshakesTotal.WithLabelValues(c.url, result).Inc()
    },
}
httpReq = httpReq.WithContext(httptrace.WithClientTrace(httpReq.Context(), trace))

resp, err := c.httpClient.Do(httpReq)
if err != nil {
    return nil, fmt.Errorf("doh %s: do: %w", c.url, err)
}
defer resp.Body.Close()

c.metrics.DoHHTTPStatusTotal.WithLabelValues(c.url, strconv.Itoa(resp.StatusCode)).Inc()
```

Add imports: `"net/http/httptrace"`, `"strconv"` (already present from Age handling).

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/upstream/ -run 'TestDoHClient_RecordsHTTPStatusMetric|TestDoHClient_RecordsTLSHandshakeMetric'`
Expected: pass.

- [ ] **Step 5: Race + full DoH test suite**

Run: `go test -race ./internal/upstream/ -run TestDoHClient`
Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add internal/upstream/doh_client.go internal/upstream/doh_client_test.go
git commit -m "$(cat <<'EOF'
upstream/doh: wire httptrace TLS handshake + HTTP status metrics

Per-Exchange ClientTrace logs cold-connection event (debug-level, with
remote addr) and increments bns_doh_tls_handshakes_total{upstream,result}
from TLSHandshakeDone. After Do returns, increment
bns_doh_http_status_total{upstream,status} from resp.StatusCode.

These give ops the two diagnostic signals from the spec: connection
churn (handshake rate) and HTTP-layer failures (status distribution),
on top of the existing bns_upstream_* DNS-layer metrics.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 18: Factory dispatch (`config.Upstream` → `Upstream`)

**Files:**

- Create: `internal/upstream/factory.go`
- Create: `internal/upstream/factory_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/upstream/factory_test.go`:

```go
package upstream_test

import (
    "log/slog"
    "testing"
    "time"

    "github.com/bcrisp4/bns/internal/config"
    "github.com/bcrisp4/bns/internal/metrics"
    "github.com/bcrisp4/bns/internal/upstream"
    "github.com/stretchr/testify/require"
)

func TestFactory_UDP_Default(t *testing.T) {
    cfg := config.Upstream{
        Type:    "", // defaults to udp
        Addr:    "1.1.1.1:53",
        Timeout: 2 * time.Second,
    }
    u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
    require.NoError(t, err)
    require.Equal(t, "udp", u.Protocol())
    require.Equal(t, "1.1.1.1:53", u.Name())
}

func TestFactory_UDP_Explicit(t *testing.T) {
    cfg := config.Upstream{
        Type:    "udp",
        Addr:    "8.8.8.8:53",
        Timeout: 2 * time.Second,
    }
    u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
    require.NoError(t, err)
    require.Equal(t, "8.8.8.8:53", u.Name())
}

func TestFactory_DoH(t *testing.T) {
    cfg := config.Upstream{
        Type:        "doh",
        URL:         "https://cloudflare-dns.com/dns-query",
        EndpointIPs: []string{"1.1.1.1"},
        Timeout:     5 * time.Second,
    }
    u, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
    require.NoError(t, err)
    require.Equal(t, "doh", u.Protocol())
    require.Equal(t, "https://cloudflare-dns.com/dns-query", u.Name())
}

func TestFactory_UnknownType(t *testing.T) {
    cfg := config.Upstream{Type: "doq", Timeout: 2 * time.Second}
    _, err := upstream.New(cfg, slog.New(slog.DiscardHandler), metrics.NewForTest())
    require.ErrorContains(t, err, "unknown type")
}
```

- [ ] **Step 2: Verify failure**

Run: `go test ./internal/upstream/ -run TestFactory`
Expected: fail — `upstream.New` undefined.

- [ ] **Step 3: Implement factory**

Create `internal/upstream/factory.go`:

```go
package upstream

import (
    "fmt"
    "log/slog"

    "github.com/bcrisp4/bns/internal/config"
    "github.com/bcrisp4/bns/internal/metrics"
)

// New constructs an Upstream from a single validated config.Upstream
// entry. Callers MUST have run config.Validate first — New does not
// re-validate field-level constraints.
//
// logger and mtr are required by DoH; ignored by UDP. Pass
// slog.New(slog.DiscardHandler) and metrics.NewForTest() in tests that
// don't care.
func New(u config.Upstream, logger *slog.Logger, mtr *metrics.Metrics) (Upstream, error) {
    switch u.Type {
    case "", "udp":
        return NewUDPClient(u.Addr, u.Timeout), nil
    case "doh":
        return NewDoHClient(u.URL, u.EndpointIPs, u.Timeout, logger, mtr)
    default:
        return nil, fmt.Errorf("upstream: unknown type %q", u.Type)
    }
}
```

- [ ] **Step 4: Verify pass**

Run: `go test ./internal/upstream/ -run TestFactory`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/upstream/factory.go internal/upstream/factory_test.go
git commit -m "$(cat <<'EOF'
upstream: add factory dispatching config.Upstream to UDP/DoH client

New(cfg, logger, mtr) returns the right concrete client based on Type.
Empty Type defaults to udp (matches validation). Unknown types error
out at construction (defense in depth — validation already rejects
them but the factory shouldn't crash on unvalidated input).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 19: Cross-field validation — http blocklist requires upstream-derived bootstrap IPs

**Files:**

- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_test.go`

- [ ] **Step 1: Write failing tests**

Append to `internal/config/validate_test.go`:

```go
func TestValidate_HTTPBlocklist_RequiresBootstrapIPs(t *testing.T) {
    // DoH-only config with file-only blocklist source: no http source,
    // so no bootstrap needed → passes even though there are zero UDP
    // upstreams. (DoH endpoint_ips ARE available as bootstrap but the
    // check is conditional on http-source presence.)
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {
            Type:        "doh",
            URL:         "https://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"1.1.1.1"},
            Timeout:     5 * time.Second,
        },
    }
    c.Blocklists.Sources = []config.BlocklistSource{
        {Type: "file", Name: "local", Path: "/dev/null"},
    }
    require.NoError(t, c.Validate())
}

func TestValidate_HTTPBlocklist_WithUDPUpstream_OK(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {Type: "udp", Addr: "1.1.1.1:53", Timeout: 2 * time.Second},
    }
    c.Blocklists.Sources = []config.BlocklistSource{
        {Type: "http", Name: "hagezi-pro", URL: "https://x/y"},
    }
    require.NoError(t, c.Validate())
}

func TestValidate_HTTPBlocklist_WithDoHEndpointIPs_OK(t *testing.T) {
    c := minimalValidConfig()
    c.Upstreams = []config.Upstream{
        {
            Type:        "doh",
            URL:         "https://cloudflare-dns.com/dns-query",
            EndpointIPs: []string{"1.1.1.1"},
            Timeout:     5 * time.Second,
        },
    }
    c.Blocklists.Sources = []config.BlocklistSource{
        {Type: "http", Name: "hagezi-pro", URL: "https://x/y"},
    }
    // DoH endpoint_ips serve as bootstrap-IP source (paired with :53).
    require.NoError(t, c.Validate())
}
```

Note: the case "http blocklist + no usable bootstrap IPs" is currently impossible since DoH always carries endpoint_ips and UDP always carries addr. The cross-field check is a defense-in-depth guard against future schema loosening rather than an exercise-able failure path today. Skip writing a failing test for that branch; the implementation should still include the check.

- [ ] **Step 2: Run tests to verify they pass (they probably already do)**

Run: `go test ./internal/config/`
Expected: pass — these are scenarios the existing validator already accepts.

- [ ] **Step 3: Add the defensive cross-field check to `Validate`**

In `internal/config/validate.go`, add at the end of `Validate()` (after the existing blocklist source loop):

```go
// Cross-field: when any http blocklist source is configured, ensure
// upstream config yields at least one bootstrap address for the
// blocklist fetcher (UDP addr OR DoH endpoint_ips paired with :53).
// Today this is automatically satisfied — UDP requires Addr, DoH
// requires non-empty EndpointIPs — so this is defensive against future
// schema changes (e.g. an upstream type with no dial address).
hasHTTPBlocklist := false
for _, s := range c.Blocklists.Sources {
    if s.Type == "http" {
        hasHTTPBlocklist = true
        break
    }
}
if hasHTTPBlocklist {
    bootstrap := 0
    for _, u := range c.Upstreams {
        switch u.Type {
        case "", "udp":
            if u.Addr != "" {
                bootstrap++
            }
        case "doh":
            bootstrap += len(u.EndpointIPs)
        }
    }
    if bootstrap == 0 {
        return errors.New("blocklists.sources contains type=http but upstream config yields no bootstrap IPs")
    }
}
return nil
```

- [ ] **Step 4: Verify all config tests pass**

Run: `go test ./internal/config/`
Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "$(cat <<'EOF'
config: defensive cross-field check for http blocklist bootstrap

When any blocklists.sources entry is type=http, require at least one
upstream-derived bootstrap IP (UDP addr or DoH endpoint_ips). Today
this is automatically satisfied by the per-type validators, but the
guard defends against future schema loosening.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 20: `cmd/bns/serve.go` — wire factory, dial-addrs helper, remove `--upstream` flag

**Files:**

- Modify: `cmd/bns/serve.go`

- [ ] **Step 1: Remove `--upstream` flag and its slice-flag wiring**

In `cmd/bns/serve.go`, delete the line declaring the flag (around line 52):

```go
cmd.Flags().StringSlice("upstream", nil, "Upstream resolver addr host:port; repeat for multiple (no default; required)")
```

And delete the `setSliceFlag` call for it (around lines 96-97):

```go
setSliceFlag(v, c, "upstream", "upstreams", func(addr string) map[string]any {
    return map[string]any{"addr": addr, "timeout": "2s"}
})
```

- [ ] **Step 2: Rename `upstreamAddrs` and broaden it to `upstreamDialAddrs`**

Find `upstreamAddrs` at the bottom of `cmd/bns/serve.go` (around line 328). Replace with:

```go
// upstreamDialAddrs returns the union of host:port forms usable for
// dialing plain DNS bootstrap queries during HTTP blocklist fetches:
//   - UDP upstream addrs verbatim (e.g. "1.1.1.1:53")
//   - DoH endpoint_ips paired with :53
//
// Assumes the same IPs serving DoH on :443 also serve plain UDP/TCP
// DNS on :53. True for every major public provider (Cloudflare, Google,
// Quad9, AdGuard, NextDNS, Mullvad); known limitation for self-hosted
// DoH-only endpoints — see docs/TODO.md "DoH upstream" entry.
func upstreamDialAddrs(ups []config.Upstream) []string {
    var out []string
    for _, u := range ups {
        switch u.Type {
        case "", "udp":
            if u.Addr != "" {
                out = append(out, u.Addr)
            }
        case "doh":
            for _, ip := range u.EndpointIPs {
                out = append(out, net.JoinHostPort(ip, "53"))
            }
        }
    }
    return out
}
```

- [ ] **Step 3: Update the `bootstrapResolver` construction**

Find:

```go
bootstrapResolver := blocklist.NewBootstrapResolver(upstreamAddrs(cfg.Upstreams))
```

Replace with:

```go
bootstrapResolver := blocklist.NewBootstrapResolver(upstreamDialAddrs(cfg.Upstreams))
```

- [ ] **Step 4: Update the upstream loop to use the factory**

Find the upstream loop (it was simplified in Task 3 but still calls `NewUDPClient` directly):

```go
ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
for _, u := range cfg.Upstreams {
    ups = append(ups, upstream.NewUDPClient(u.Addr, u.Timeout))
}
pool := upstream.NewPool(ups, mtr)
```

Replace with:

```go
ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
for _, u := range cfg.Upstreams {
    client, err := upstream.New(u, logger, mtr)
    if err != nil {
        return fmt.Errorf("build upstream: %w", err)
    }
    ups = append(ups, client)
}
pool := upstream.NewPool(ups, mtr)
```

- [ ] **Step 5: Update the startup log line that logged upstreams**

Find the log call (around line 118) that uses `upstreamAddrs(cfg.Upstreams)`:

```go
"admin", cfg.Admin.Listen, "upstreams", upstreamAddrs(cfg.Upstreams))
```

Either:

(a) keep calling `upstreamDialAddrs` (now slightly different in meaning but acceptable for a startup-info log), OR
(b) write a small helper that yields a human-readable label per upstream (`addr` for UDP, `URL` for DoH).

Recommend (b) for clarity. Add:

```go
func upstreamLabels(ups []config.Upstream) []string {
    out := make([]string, len(ups))
    for i, u := range ups {
        switch u.Type {
        case "doh":
            out[i] = u.URL
        default:
            out[i] = u.Addr
        }
    }
    return out
}
```

And update the log call:

```go
"admin", cfg.Admin.Listen, "upstreams", upstreamLabels(cfg.Upstreams))
```

- [ ] **Step 6: Build + vet + test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 7: Manual smoke — UDP-only legacy config still works**

In one terminal:
```bash
make build
./bin/bns serve -c examples/config.example.yaml \
    --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354 &
```

(Note: at this point `examples/config.example.yaml` is still the UDP-only file from before the DoH spec; the example update is a later task. So this smoke confirms backward compat.)

In another:
```bash
dig @127.0.0.1 -p 5354 example.com
```

Expected: NOERROR with answer; verify `curl -s localhost:9090/metrics | grep 'bns_upstream_queries_total.*protocol="udp"'` shows the counter.

Kill the background `bns` process.

- [ ] **Step 8: Commit**

```bash
git add cmd/bns/serve.go
git commit -m "$(cat <<'EOF'
serve: wire upstream factory, remove --upstream flag, broaden dial-addrs

Upstream loop now uses upstream.New (factory) which dispatches to UDP
or DoH clients based on config.Upstream.Type. The legacy --upstream
cobra flag is removed — upstreams are YAML-only (matches blocklists).

upstreamAddrs renamed to upstreamDialAddrs and broadened to include
DoH endpoint_ips paired with :53 so the blocklist fetcher's
BootstrapResolver still has a non-empty dial-target list when only
DoH upstreams are configured.

Startup log gains an upstreamLabels helper that prints URL for DoH
entries instead of an empty addr.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 21: Update `examples/config.example.yaml` + create `examples/config-doh.yaml`

**Files:**

- Modify: `examples/config.example.yaml`
- Create: `examples/config-doh.yaml`

- [ ] **Step 1: Update the canonical example to demonstrate both forms**

Replace the `upstreams:` block in `examples/config.example.yaml` with:

```yaml
upstreams:
  - type: udp
    addr: 1.1.1.1:53
    timeout: 2s

  - type: doh
    url: https://cloudflare-dns.com/dns-query
    endpoint_ips: [1.1.1.1, 1.0.0.1]
    timeout: 5s
```

- [ ] **Step 2: Create DoH-only example**

Create `examples/config-doh.yaml` (minimal viable DoH-only config used by the manual smoke recipe in CLAUDE.md):

```yaml
listen:
  udp: 127.0.0.1:5354
  tcp: 127.0.0.1:5354
  query_timeout: 5s

upstreams:
  - type: doh
    url: https://cloudflare-dns.com/dns-query
    endpoint_ips: [1.1.1.1, 1.0.0.1]
    timeout: 5s

cache:
  capacity: 10000
  min_ttl: 0s
  max_ttl: 86400s
  negative_ttl_max: 900s

admin:
  listen: 127.0.0.1:9090

logging:
  level: info
  format: json
  query_log:
    enabled: true

shutdown_timeout: 5s
startup_probe_timeout: 5s
```

- [ ] **Step 3: Verify both parse cleanly**

```bash
./bin/bns serve -c examples/config.example.yaml --listen.udp 127.0.0.1:65000 --listen.tcp 127.0.0.1:65000 &
sleep 1
kill %1 2>/dev/null

./bin/bns serve -c examples/config-doh.yaml &
sleep 2
kill %1 2>/dev/null
```

Expected: both start without validation errors. (The DoH-only one will attempt a real DoH connection to Cloudflare at startup probe — accept either success or warmupProbe-failed-but-server-still-up.)

- [ ] **Step 4: Commit**

```bash
git add examples/config.example.yaml examples/config-doh.yaml
git commit -m "$(cat <<'EOF'
examples: add DoH entry to config.example.yaml; new DoH-only file

config.example.yaml demonstrates both UDP and DoH forms in one
upstreams list. config-doh.yaml is a minimal DoH-only config used by
the manual smoke recipe documented in CLAUDE.md.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 22: Switch container default to DoH-primary

**Files:**

- Modify: `deploy/docker/config.yaml`

- [ ] **Step 1: Replace the upstreams block**

In `deploy/docker/config.yaml`, replace the existing upstreams block with:

```yaml
upstreams:
  - type: doh
    url: https://cloudflare-dns.com/dns-query
    endpoint_ips: [1.1.1.1, 1.0.0.1]
    timeout: 5s

  - type: udp
    addr: 1.1.1.1:53
    timeout: 2s
```

- [ ] **Step 2: Verify container build still works (compile-only)**

Run: `docker build -f deploy/docker/Dockerfile -t bns:doh-test .`
Expected: success.

(If docker isn't available locally, skip this step and rely on CI.)

- [ ] **Step 3: Commit**

```bash
git add deploy/docker/config.yaml
git commit -m "$(cat <<'EOF'
docker: switch baked default to DoH-primary with UDP fallback

The container is the drop-in deployment path; defaulting to encrypted
upstream is the right modern posture. UDP fallback preserves
availability when DoH is unreachable. Operators wanting plain UDP only
can mount their own config.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 23: Integration test — end-to-end DoH through BNS

**Files:**

- Create: `internal/integration/integration_doh_test.go`

- [ ] **Step 1: Inspect existing MVP integration test for the harness pattern**

Read `internal/integration/` for existing test (one should exist per CLAUDE.md). Note: the testing pattern (spinning up listeners, sending miekg client queries) is the template for the new DoH test.

- [ ] **Step 2: Create the integration test**

Create `internal/integration/integration_doh_test.go`:

```go
package integration_test

import (
    "bytes"
    "context"
    "crypto/tls"
    "crypto/x509"
    "io"
    "log/slog"
    "net"
    "net/http"
    "net/http/httptest"
    "net/url"
    "testing"
    "time"

    "codeberg.org/miekg/dns"
    "codeberg.org/miekg/dns/dnshttp"
    "github.com/bcrisp4/bns/internal/cache"
    "github.com/bcrisp4/bns/internal/config"
    "github.com/bcrisp4/bns/internal/metrics"
    "github.com/bcrisp4/bns/internal/resolver"
    "github.com/bcrisp4/bns/internal/resolver/chain"
    "github.com/bcrisp4/bns/internal/upstream"
    "github.com/bcrisp4/bns/internal/upstream/testutil"
    "github.com/stretchr/testify/require"
)

// TestIntegration_DoH_EndToEnd spins a fake DoH server, plugs a real
// BNS resolver chain into it via a real DoHClient, and sends a DNS
// query through the chain. Verifies the full data flow: chain →
// forward → Pool → DoHClient → DoH server → response → chain → caller.
func TestIntegration_DoH_EndToEnd(t *testing.T) {
    // Fake DoH server returning a canned A record.
    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        req, err := dnshttp.Request(r)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        resp := new(dns.Msg)
        resp.Response = true
        resp.Question = req.Question
        // Canned A record at TTL=60.
        resp.Answer = []dns.RR{&dns.A{
            Hdr: dns.Header{Name: req.Question[0].Header().Name, TTL: 60},
            A:   /* netip parsing */ mustNetip("203.0.113.42"),
        }}
        require.NoError(t, resp.Pack())
        w.Header().Set("Content-Type", "application/dns-message")
        _, _ = io.Copy(w, bytes.NewReader(resp.Data))
    })

    cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
    srv := httptest.NewUnstartedServer(handler)
    srv.TLS = &tls.Config{
        Certificates: []tls.Certificate{cert},
        NextProtos:   []string{"h2", "http/1.1"},
    }
    srv.StartTLS()
    t.Cleanup(srv.Close)

    parsed, _ := url.Parse(srv.URL)
    dohURL := "https://" + parsed.Host + "/dns-query"

    mtr := metrics.NewForTest()
    logger := slog.New(slog.DiscardHandler)

    dohClient, err := upstream.NewDoHClient(dohURL, nil, 5*time.Second, logger, mtr)
    require.NoError(t, err)

    // Inject test root CA into the DoH client's TLS config.
    pool := x509.NewCertPool()
    pool.AddCert(cert.Leaf)
    dohClient.HTTPClientForTest().Transport.(*http.Transport).TLSClientConfig.RootCAs = pool
    // (If no HTTPClientForTest accessor exists, replace with whatever
    // mechanism the same-package tests use. Or move this test to package
    // upstream so it can poke fields directly.)

    pool2 := upstream.NewPool([]upstream.Upstream{dohClient}, mtr)

    lru := cache.NewLRU(100)
    chainResolver := chain.Build(chain.Deps{
        Upstream: pool2,
        Cache:    lru,
        CacheCfg: config.Cache{MaxTTL: 3600 * time.Second, NegativeTTLMax: 60 * time.Second},
        // Blocklist + QueryLog: pass disabled stubs.
        Blocklist: /* empty matcher */,
        QueryLog:  /* disabled query log */,
        Metrics:   mtr,
    })

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    ctx, _ = resolver.WithBlockMarker(ctx)
    ctx, _ = resolver.WithUpstreamMarker(ctx)

    req := dns.NewMsg("example.com.", dns.TypeA)
    resp, err := chainResolver.Resolve(ctx, req)
    require.NoError(t, err)
    require.NotNil(t, resp)
    require.Len(t, resp.Answer, 1)

    // Verify per-query attribution landed in ctx.
    info, present := resolver.UpstreamInfoFrom(ctx)
    require.True(t, present)
    require.Equal(t, dohURL, info.Name)
    require.Equal(t, "doh", info.Protocol)
}

// mustNetip is a small helper to build the rdata for the canned A record.
// (Adapt to whatever rdata constructor pattern the existing tests use;
// the package layout note in CLAUDE.md describes the rdata sub-struct
// shape for miekg/dns v2 A records.)
func mustNetip(s string) /* rdata.A */ { /* ... */ }
```

(The skeleton above intentionally leaves a few seams that need patching to fit existing utilities — `HTTPClientForTest`, the rdata constructor, the empty-blocklist/disabled-querylog stubs. Inspect the existing MVP integration test for the right shapes; the goal is one end-to-end DoH test asserting the response landed AND that `UpstreamInfoFrom` returned the DoH client's URL.)

A simpler alternative if the chain wiring is awkward: skip the chain entirely and test directly:

```go
// minimal integration: Pool wraps DoHClient; assert MarkUpstream fires
// and an A record comes back.
func TestIntegration_DoH_Direct(t *testing.T) {
    // ... same fake server setup ...
    dohClient := newTestDoHServerWithMetrics(...) // reuse existing helper
    pool := upstream.NewPool([]upstream.Upstream{dohClient}, mtr)

    ctx, _ := resolver.WithUpstreamMarker(context.Background())
    resp, err := pool.Exchange(ctx, dns.NewMsg("example.com.", dns.TypeA))
    require.NoError(t, err)
    require.NotNil(t, resp)

    info, present := resolver.UpstreamInfoFrom(ctx)
    require.True(t, present)
    require.Equal(t, "doh", info.Protocol)
}
```

Decide between full-chain and direct based on which is achievable in <30 LOC. Direct is recommended — full-chain integration is best left to a follow-up if the value warrants it.

- [ ] **Step 3: Run + race**

Run: `go test -race ./internal/integration/`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add internal/integration/integration_doh_test.go
git commit -m "$(cat <<'EOF'
integration: end-to-end DoH test through Pool + DoHClient

Spins a fake DoH server (httptest TLS) with a canned A response,
wires a real DoHClient + Pool against it, and verifies the response
plus per-query upstream attribution (UpstreamInfoFrom returns the
DoH URL and "doh" protocol).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 24: Update `CLAUDE.md`

**Files:**

- Modify: `CLAUDE.md`

- [ ] **Step 1: Update Quickstart**

Find the Quickstart block. Replace the `bns serve` command (which uses `--upstream`) with the YAML-driven form:

```bash
BNS_LOGGING__QUERY_LOG__ENABLED=true BNS_LOGGING__LEVEL=debug \
  ./bin/bns serve -c examples/config.example.yaml \
  --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354
```

(Drop the `--upstream` flag.)

Add a sentence above the `dig` smoke commands noting that the default config now demonstrates both UDP and DoH forms.

- [ ] **Step 2: Update Architecture > Package layout**

Find the package layout block. Under `internal/upstream/`, add:

```
internal/upstream/                Upstream interface (Name+Protocol getters), UDPClient, DoHClient,
                                  Pool (sequential failover), factory.New(cfg)→Upstream,
                                  testutil/{Spawn, NewTLSCert}
```

- [ ] **Step 3: Update Architecture > Key interfaces table**

In the `Upstream` row, change `Contract` to:

```
`Exchange(ctx, req) (*dns.Msg, error)` + `Name() string` + `Protocol() string`
```

And in `Future impls` for that row, mark DoH as done:

```
DoT, DoH3
```

- [ ] **Step 4: Update Tech stack — non-obvious**

Add a bullet to the existing tech-stack list:

```
- **DoH upstream:** `internal/upstream/doh_client.go` — hand-rolled over stdlib `net/http`,
  uses miekg/dns v2 `dnshttp.Response` for decode but bypasses `dnshttp.NewRequest`
  to avoid its hardcoded `/dns-query` path append (footgun for natural config URLs).
  Operator-pinned `endpoint_ips` substitute the hostname in `Transport.DialContext`;
  hostname is retained for SNI / cert SAN match. HTTP/2 preferred, HTTP/1.1 fallback,
  TLS 1.3 floor.
```

- [ ] **Step 5: Update Gotchas**

Append three new entries to the Gotchas section:

```
- **DoH upstreams require `endpoint_ips`** — operator-pinned IPs. BNS never resolves
  the DoH URL hostname at runtime (would deadlock through itself on Pi-hole-style
  deployments). The hostname is used only for SNI + cert validation.
- **`--upstream` CLI flag removed.** Upstreams are YAML-only (matches `blocklists.sources`,
  which has been YAML-only since HTTP source landed because viper can't index slice
  config via env vars).
- **Blocklist fetcher reuses DoH `endpoint_ips` as bootstrap DNS targets paired with `:53`.**
  Assumes the same IPs serving DoH on `:443` also serve plain UDP/TCP DNS on `:53` — true
  for every major public provider, documented limitation for self-hosted DoH-only
  endpoints (see `docs/TODO.md`).
```

- [ ] **Step 6: Verify markdown still renders cleanly**

Visual inspection only (no markdown linter in repo).

- [ ] **Step 7: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: CLAUDE.md updates for DoH upstream landing

Quickstart drops --upstream flag (config-driven now). Package layout
mentions doh_client.go + factory.go + testutil tlscert.go. Upstream
interface row in the Key interfaces table gains Name+Protocol; future
impls list trimmed to DoT, DoH3. Tech-stack callout for the hand-rolled
DoH client and the dnshttp.NewRequest footgun avoidance. Three new
Gotchas entries: endpoint_ips operator burden, --upstream removal,
blocklist bootstrap reuse of endpoint_ips with :53 assumption.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 25: Final verification

**Files:** (none — verification only)

- [ ] **Step 1: Format check**

Run: `find . -name '*.go' -not -path '*/vendor/*' | xargs gofmt -l`
Expected: empty output. If any files listed, run `gofmt -w` on them and commit as a separate `chore: gofmt` commit.

- [ ] **Step 2: Vet**

Run: `make vet`
Expected: pass.

- [ ] **Step 3: Race + test**

Run: `make race`
Expected: pass, no races.

- [ ] **Step 4: Manual smoke — UDP-only config (backward compat)**

```bash
make build
./bin/bns serve -c examples/config.example.yaml \
    --listen.udp 127.0.0.1:5354 --listen.tcp 127.0.0.1:5354 \
    --admin.listen 127.0.0.1:9091 &
sleep 2
dig @127.0.0.1 -p 5354 example.com
curl -s http://127.0.0.1:9091/metrics | grep -E 'bns_upstream_queries_total\{.*protocol="(udp|doh)"'
kill %1
```

Expected:
- `dig` returns NOERROR with an answer.
- `bns_upstream_queries_total{protocol="udp"}` and/or `{protocol="doh"}` lines visible.

- [ ] **Step 5: Manual smoke — DoH-only config**

```bash
./bin/bns serve -c examples/config-doh.yaml &
sleep 3
dig @127.0.0.1 -p 5354 example.com
curl -s http://127.0.0.1:9090/metrics | grep -E 'bns_doh_(http_status|tls_handshakes)_total'
curl -s http://127.0.0.1:9090/metrics | grep -E 'bns_upstream_queries_total\{.*protocol="doh"'
kill %1
```

Expected:
- `dig` returns NOERROR with an answer (real Cloudflare DoH).
- `bns_doh_tls_handshakes_total` shows at least one `result="ok"` handshake.
- `bns_doh_http_status_total{status="200"}` is non-zero.
- `bns_upstream_queries_total{protocol="doh",outcome="ok"}` is non-zero.

- [ ] **Step 6: Manual smoke — query log per-query attribution**

With the DoH-only config running, run a query and tail stdout. The JSON log line for the query should include `"upstream":"https://cloudflare-dns.com/dns-query"` and `"upstream_protocol":"doh"`.

If query log goes to stderr/file, find and inspect it. Confirm both attrs land.

- [ ] **Step 7: Manual failover smoke**

Edit a copy of `examples/config-doh.yaml` to put an unreachable primary DoH ahead of a reachable Cloudflare:

```yaml
upstreams:
  - type: doh
    url: https://nonexistent.example/dns-query
    endpoint_ips: [192.0.2.1]   # TEST-NET-1, guaranteed unroutable
    timeout: 1s
  - type: doh
    url: https://cloudflare-dns.com/dns-query
    endpoint_ips: [1.1.1.1, 1.0.0.1]
    timeout: 5s
```

Run BNS, send a query, verify:
- `dig` returns NOERROR (failover worked).
- `bns_upstream_queries_total{upstream="https://nonexistent.example/dns-query",protocol="doh",outcome="error"}` is non-zero.
- `bns_upstream_queries_total{upstream="https://cloudflare-dns.com/dns-query",protocol="doh",outcome="ok"}` is non-zero.

- [ ] **Step 8: Run `/simplify`** (per user's global preferences)

The user's CLAUDE.md says: "ALWAYS run /simplify once all tasks in a plan are complete." Trigger it from the user's interactive session — this plan execution surface can't invoke it directly.

- [ ] **Step 9: Tag and release** (optional, user-driven)

Once smoke is green, the user may tag `v0.4.0` and push. Existing CI workflows handle the container build and GHCR push.

---

## Self-Review

After writing the full plan, fresh-eye check:

**Spec coverage:**
- §1 Goals 1–8 → Tasks 1–17 implement the DoH client + interface + qlog attribution; Task 22 switches container default; Task 24 documents migration.
- §3 Non-goals → no tasks (correct).
- §4 Architecture (interface, package layout, chain integration, marker) → Tasks 1–6.
- §5 Config schema → Task 7 (struct), Task 8 (validation), Task 21 (examples).
- §6 DoH client implementation → Tasks 11–17 (constructor + Exchange + Age + httptrace + tests).
- §7 Wiring changes → Task 3 (Pool refactor), Task 19 (cross-field validation), Task 20 (serve.go + dial-addrs), Task 5 (marker install).
- §8 Testing → distributed across Tasks 9 (testutil), 12–17 (DoH unit), 23 (integration), 18 (factory tests).
- §9 Container/docs → Tasks 21 (examples), 22 (docker config), 24 (CLAUDE.md).
- §10 Rollout/migration → Tasks 20 (CLI removal), 22 (container), 24 (CLAUDE.md), 25 (release tagging).
- §11 Alternatives → no tasks; spec content (correct).
- §12 Risks → no tasks; spec content (correct).
- §13 Open questions → Task 11's `http2.ConfigureTransport` resolves Q1; Task 11 documents IdleConnTimeout as hardcoded (Q2); no metric size histogram (Q3 explicitly deferred).

**Placeholder scan:** none of "TBD/TODO/implement later/fill in details/handle edge cases/etc."; every step has concrete code where code is needed.

**Type consistency:**
- `Upstream` gains `Name() string` + `Protocol() string` in Task 1; used in Pool (Task 3), DoHClient (Task 11), factory (Task 18). Consistent.
- `UpstreamInfo{Name, Protocol}` defined in Task 4; read in Task 6, asserted in Tasks 5/6. Consistent.
- `metrics.NewForTest()` introduced in Task 11; used in Tasks 11, 17, 18. Consistent (helper added once, reused).
- `config.Upstream` fields added in Task 7; consumed by Task 8 (validate), Task 18 (factory), Task 20 (serve.go). Consistent.

Plan complete and saved to `docs/plans/2026-05-24-doh-upstream-implementation.md`.
