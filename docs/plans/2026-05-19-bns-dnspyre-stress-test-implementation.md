# BNS dnspyre stress-test harness — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a single Go binary (`bns-stress`) that orchestrates a mock upstream and BNS, drives load via the imported dnspyre library, and produces a timestamped report directory per run.

**Architecture:** dnspyre is imported as a library (`github.com/tantalor93/dnspyre/v3/pkg/dnsbench`). The orchestrator spawns `bns serve` and `mockupstream` as subprocesses (local topology) or talks to externally-running instances (remote topology). It scrapes `/metrics` before+after, captures pprof, calls `dnsbench.Benchmark.Run(ctx)`, then renders a Markdown report. Per the spec at `docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md`.

**Tech Stack:** Go 1.26, `codeberg.org/miekg/dns` v2 (BNS + mockupstream), `github.com/tantalor93/dnspyre/v3` (load gen, library), `github.com/HdrHistogram/hdrhistogram-go` (quantiles, transitively via dnspyre), `github.com/stretchr/testify/require` (tests).

**Reference reading before starting:**
- `docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md` — the design.
- `CLAUDE.md` — project conventions, miekg/dns v2 gotchas, architecture invariants.
- `internal/upstream/testutil/server.go` — the canonical `dns.Server` ready-sync pattern; mirror it in `cmd/mockupstream`.
- `internal/admin/server.go` — admin mux to extend with optional pprof.
- `cmd/bns/serve.go` — flag wiring patterns.

---

## File structure

**Create:**
- `cmd/mockupstream/main.go` — entry point: parse flags, build handler, run UDP+TCP `dns.Server` pair, wait for signal.
- `cmd/mockupstream/handler.go` — pure handler function (per-qtype canned answers, latency, drop-rate).
- `cmd/mockupstream/handler_test.go` — unit tests for handler.
- `cmd/mockupstream/main_test.go` — small end-to-end test that spawns the binary and queries it.
- `cmd/bns-stress/main.go` — entry point: parse flags, build `stress.Config`, call `stress.Run(ctx, cfg)`.
- `cmd/bns-stress/main_test.go` — build tag `stress_integration`; full local run.
- `internal/stress/config.go` — `Config` struct + `Defaults()` + `Validate()`.
- `internal/stress/config_test.go` — validation cases.
- `internal/stress/scenario.go` — `Scenario` struct + `Registry()` + `Get(name)`.
- `internal/stress/scenarios/mixed.go` — `New() stress.Scenario` for the mixed scenario.
- `internal/stress/scenarios/mixed_test.go` — golden compare of the built `dnsbench.Benchmark`.
- `internal/stress/metrics.go` — `Snapshot` parse + `Diff` helper + quantile computation.
- `internal/stress/metrics_test.go` — fixture-driven diff tests.
- `internal/stress/metrics_fixtures_test.go` — embedded `before.prom`/`after.prom` for tests.
- `internal/stress/report.go` — template + `Render(ReportInput) string`.
- `internal/stress/report_test.go` — golden render test.
- `internal/stress/report_golden_test.go` — `report_golden.md` fixture under `testdata/`.
- `internal/stress/orchestrator.go` — `Run(ctx, cfg)`; subprocess spawn, ready-wait, pprof capture, sequencing, teardown.
- `internal/stress/orchestrator_test.go` — unit tests for URL builders, env composition.
- `internal/stress/testdata/` — fixture files for tests.
- `scripts/stress/build_mixed.sh` — generator script (documentation; produces the committed files).
- `scripts/stress/queries/mixed.txt` — generated + committed pool.
- `scripts/stress/queries/blocked-sample.txt` — generated + committed hagezi subset.

**Modify:**
- `cmd/bns/serve.go` — add `--pprof` flag; pass through to admin server.
- `internal/admin/server.go` — extend `New()` to optionally mount `net/http/pprof` handlers.
- `internal/admin/server_test.go` — add pprof-enabled case.
- `Makefile` — add `build-stress`, `stress`, `stress-server`, `stress-clean` targets.
- `.gitignore` — add `dist/` if not already present.
- `go.mod` / `go.sum` — `go get github.com/tantalor93/dnspyre/v3@v3.11.0`.

---

## Task 1: Foundation — gitignore, dnspyre dep, Makefile skeleton

**Files:**
- Modify: `.gitignore`
- Modify: `go.mod`, `go.sum`
- Modify: `Makefile`

- [ ] **Step 1: Inspect current `.gitignore`**

Run: `cat .gitignore`
Expected: confirm whether `dist/` already listed.

- [ ] **Step 2: Add `dist/` to `.gitignore` if absent**

If not present, append:

```
dist/
```

- [ ] **Step 3: Add dnspyre as a dependency**

Run: `go get github.com/tantalor93/dnspyre/v3@v3.11.0`
Expected: `go.mod` gains `require github.com/tantalor93/dnspyre/v3 v3.11.0` and `go.sum` populates.

- [ ] **Step 4: Confirm dnspyre is not in `cmd/bns` import closure**

Run: `go mod why github.com/tantalor93/dnspyre/v3`
Expected: output references only `cmd/bns-stress` or `internal/stress` paths (initially, before those packages exist, `go mod why` will say it is unreferenced; that's fine — we'll re-check in the final integration task).

- [ ] **Step 5: Add Makefile targets**

Append to `Makefile`:

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

Also update `.PHONY` at top of file: add `build-stress stress stress-server stress-clean` to the existing list.

- [ ] **Step 6: Verify `make build-stress` fails as expected**

Run: `make build-stress`
Expected: FAIL — `./cmd/mockupstream` and `./cmd/bns-stress` do not exist yet. This is the intended state.

- [ ] **Step 7: Commit**

```bash
git add .gitignore go.mod go.sum Makefile
git commit -m "Add dnspyre dependency and stress Makefile targets

Wires github.com/tantalor93/dnspyre/v3@v3.11.0 into go.mod and adds
build-stress, stress, stress-server, stress-clean targets. The new
cmd/ packages will be added in subsequent commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: mockupstream — handler logic

**Files:**
- Create: `cmd/mockupstream/handler.go`
- Test: `cmd/mockupstream/handler_test.go`

- [ ] **Step 1: Write the failing test**

`cmd/mockupstream/handler_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/stretchr/testify/require"
)

func newReq(name string, qtype uint16) *dns.Msg {
	m := dns.NewMsg(name, qtype)
	m.ID = 0x1234
	return m
}

func TestHandler_A_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeA))

	require.True(t, resp.Response)
	require.Equal(t, uint16(0x1234), resp.ID)
	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)

	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", a.A.String())
	require.Equal(t, uint32(300), a.Hdr.Ttl)
}

func TestHandler_AAAA_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeAAAA))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	require.True(t, ok)
	require.Equal(t, "2001:db8::1", aaaa.AAAA.String())
}

func TestHandler_NS_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeNS))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	ns, ok := resp.Answer[0].(*dns.NS)
	require.True(t, ok)
	require.Equal(t, "ns.mock.invalid.", ns.Ns)
}

func TestHandler_SOA_UsesNegTTLForMinimum(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 90 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeSOA))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	soa, ok := resp.Answer[0].(*dns.SOA)
	require.True(t, ok)
	require.Equal(t, uint32(90), soa.Minttl)
}

func TestHandler_UnknownType_Refused(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeMX))

	require.Equal(t, uint16(dns.RcodeRefused), resp.Rcode)
	require.Empty(t, resp.Answer)
}

func TestHandler_DropRate1_ReturnsNil(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second, DropRate: 1.0})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeA))
	require.Nil(t, resp)
}

func TestHandler_NoQuestion_Refused(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	m := &dns.Msg{}
	m.ID = 1
	resp := h.Answer(context.Background(), m)
	require.Equal(t, uint16(dns.RcodeRefused), resp.Rcode)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mockupstream/...`
Expected: FAIL — `cmd/mockupstream/handler.go` does not exist.

- [ ] **Step 3: Write minimal handler implementation**

`cmd/mockupstream/handler.go`:

```go
package main

import (
	"context"
	"math/rand"
	"net"
	"time"

	"codeberg.org/miekg/dns"
)

// HandlerConfig holds the runtime knobs for the mock handler.
type HandlerConfig struct {
	TTL      time.Duration
	NegTTL   time.Duration
	Latency  time.Duration
	DropRate float64
}

// Handler answers DNS queries with canned, instant responses. It is the
// upstream substitute used by the stress harness to isolate BNS from
// network and real-recursor variability.
type Handler struct {
	cfg HandlerConfig
	rng *rand.Rand
}

// NewHandler builds a Handler with the given configuration. The PRNG is
// seeded with the current time; reseed inside tests if determinism matters.
func NewHandler(cfg HandlerConfig) *Handler {
	return &Handler{
		cfg: cfg,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Answer returns the canned response for req. It returns nil when the
// configured drop-rate triggers, signalling the wire layer to silently
// drop the query (exercises BNS timeout handling). Latency is applied
// before returning if configured.
func (h *Handler) Answer(_ context.Context, req *dns.Msg) *dns.Msg {
	if h.cfg.DropRate > 0 && h.rng.Float64() < h.cfg.DropRate {
		return nil
	}

	if h.cfg.Latency > 0 {
		time.Sleep(h.cfg.Latency)
	}

	resp := &dns.Msg{}
	resp.Response = true
	resp.ID = req.ID
	resp.Question = req.Question

	if len(req.Question) == 0 {
		resp.Rcode = uint16(dns.RcodeRefused)
		return resp
	}

	q := req.Question[0]
	name := q.Header().Name
	ttl := uint32(h.cfg.TTL.Seconds())
	negTTL := uint32(h.cfg.NegTTL.Seconds())

	switch dns.RRToType(q) {
	case dns.TypeA:
		rr := &dns.A{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
			A:   net.ParseIP("192.0.2.1").To4(),
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeAAAA:
		rr := &dns.AAAA{
			Hdr:  dns.RR_Header{Name: name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: ttl},
			AAAA: net.ParseIP("2001:db8::1"),
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeNS:
		rr := &dns.NS{
			Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: ttl},
			Ns:  "ns.mock.invalid.",
		}
		resp.Answer = append(resp.Answer, rr)
	case dns.TypeSOA:
		rr := &dns.SOA{
			Hdr:     dns.RR_Header{Name: name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: ttl},
			Ns:      "ns.mock.invalid.",
			Mbox:    "hostmaster.mock.invalid.",
			Serial:  1,
			Refresh: 3600,
			Retry:   600,
			Expire:  86400,
			Minttl:  negTTL,
		}
		resp.Answer = append(resp.Answer, rr)
	default:
		resp.Rcode = uint16(dns.RcodeRefused)
	}

	return resp
}
```

Note the miekg/dns v2 idioms from CLAUDE.md: `dns.RRToType(q)` to extract qtype from a `dns.RR`-shaped question, `Rcode` is `uint16`, capital `ID`, `RR_Header` constructed by hand (no constructor needed for fields we set).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/mockupstream/...`
Expected: PASS for all 7 subtests.

- [ ] **Step 5: Commit**

```bash
git add cmd/mockupstream/handler.go cmd/mockupstream/handler_test.go
git commit -m "Add mockupstream canned-answer handler

Per-qtype responses for A/AAAA/NS/SOA, REFUSED for anything else.
Supports DropRate (silent drop, returns nil) and Latency (sleep before
reply) knobs for future scenarios.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: mockupstream — main wiring

**Files:**
- Create: `cmd/mockupstream/main.go`
- Test: `cmd/mockupstream/main_test.go`

- [ ] **Step 1: Write the failing end-to-end test**

`cmd/mockupstream/main_test.go`:

```go
package main_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/stretchr/testify/require"
)

// findFreePort grabs an ephemeral 127.0.0.1 port and returns the host:port string.
// It closes the listener immediately so the caller can re-bind; brief race window
// is acceptable in tests.
func findFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestMockUpstream_Binary_AnswersA(t *testing.T) {
	// Build binary into a tmp file. Use go build, not the Makefile, so the
	// test is self-contained.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mockupstream")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	require.NoError(t, build.Run())

	addr := findFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--listen.udp", addr, "--listen.tcp", addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Poll until the UDP socket is answering. Bounded wait.
	c := dns.NewClient()
	c.Transport.ReadTimeout = 500 * time.Millisecond
	c.Transport.WriteTimeout = 500 * time.Millisecond
	req := dns.NewMsg("example.test.", dns.TypeA)

	deadline := time.Now().Add(5 * time.Second)
	var resp *dns.Msg
	var err error
	for time.Now().Before(deadline) {
		resp, _, err = c.Exchange(context.Background(), req, "udp", addr)
		if err == nil && resp != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Answer, 1)
	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", a.A.String())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mockupstream/... -run TestMockUpstream_Binary -v`
Expected: FAIL — `cmd/mockupstream/main.go` does not exist; build step fails.

- [ ] **Step 3: Write main wiring**

`cmd/mockupstream/main.go`:

```go
// Command mockupstream is a minimal DNS server used by the BNS stress
// harness as a substitute upstream. It answers a fixed set of qtypes
// with canned records and is intentionally not configurable beyond a
// few timing knobs. It is not a release artefact.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"codeberg.org/miekg/dns"
)

func main() {
	udpAddr := flag.String("listen.udp", "127.0.0.1:5355", "UDP listen address")
	tcpAddr := flag.String("listen.tcp", "127.0.0.1:5355", "TCP listen address")
	ttl := flag.Duration("ttl", 300*time.Second, "Positive-answer TTL")
	negTTL := flag.Duration("neg-ttl", 60*time.Second, "SOA minimum (negative-cache) TTL")
	latency := flag.Duration("latency", 0, "Artificial sleep before reply")
	dropRate := flag.Float64("drop-rate", 0.0, "Fraction of queries to silently drop")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	h := NewHandler(HandlerConfig{
		TTL:      *ttl,
		NegTTL:   *negTTL,
		Latency:  *latency,
		DropRate: *dropRate,
	})

	var served atomic.Uint64
	dnsHandler := dns.HandlerFunc(func(ctx context.Context, w dns.ResponseWriter, req *dns.Msg) {
		resp := h.Answer(ctx, req)
		if resp == nil {
			return // configured drop
		}
		if err := resp.Pack(); err != nil {
			return
		}
		_, _ = io.Copy(w, resp)
		served.Add(1)
	})

	udpConn, err := net.ListenPacket("udp", *udpAddr)
	if err != nil {
		log.Error("bind udp", "addr", *udpAddr, "err", err.Error())
		os.Exit(1)
	}
	tcpLn, err := net.Listen("tcp", *tcpAddr)
	if err != nil {
		log.Error("bind tcp", "addr", *tcpAddr, "err", err.Error())
		os.Exit(1)
	}

	udpReady := make(chan struct{})
	tcpReady := make(chan struct{})
	udpSrv := &dns.Server{
		PacketConn:        udpConn,
		Handler:           dnsHandler,
		NotifyStartedFunc: func(_ context.Context) { close(udpReady) },
	}
	tcpSrv := &dns.Server{
		Listener:          tcpLn,
		Handler:           dnsHandler,
		NotifyStartedFunc: func(_ context.Context) { close(tcpReady) },
	}

	errCh := make(chan error, 2)
	go func() { errCh <- udpSrv.ListenAndServe() }()
	go func() { errCh <- tcpSrv.ListenAndServe() }()
	<-udpReady
	<-tcpReady

	log.Info("mockupstream ready", "udp", *udpAddr, "tcp", *tcpAddr, "ttl", *ttl, "neg_ttl", *negTTL)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sigs:
		log.Info("mockupstream stopping", "signal", s.String())
	case err := <-errCh:
		if err != nil {
			log.Error("listener exited", "err", err.Error())
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = udpSrv.Shutdown(shutdownCtx)
	_ = tcpSrv.Shutdown(shutdownCtx)

	log.Info("mockupstream stopped", "served", served.Load())
	_ = fmt.Sprintf // keep fmt import if unused later
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/mockupstream/... -v`
Expected: PASS for all tests, including the binary E2E.

- [ ] **Step 5: Run race detector against the package**

Run: `go test -race ./cmd/mockupstream/...`
Expected: PASS, no data races.

- [ ] **Step 6: Build the binary via Make to confirm wiring**

Run: `make build` && `mkdir -p bin && go build -trimpath -o bin/mockupstream ./cmd/mockupstream`
Expected: builds cleanly. (Full `make build-stress` will work once `cmd/bns-stress` exists.)

- [ ] **Step 7: Commit**

```bash
git add cmd/mockupstream/main.go cmd/mockupstream/main_test.go
git commit -m "Add mockupstream binary entry point

Wires the handler from the previous commit into a UDP+TCP dns.Server
pair using the same NotifyStartedFunc ready-sync as
internal/upstream/testutil. Logs one ready line and one shutdown line.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: stress.Config — types + validation

**Files:**
- Create: `internal/stress/config.go`
- Test: `internal/stress/config_test.go`

- [ ] **Step 1: Write the failing test**

`internal/stress/config_test.go`:

```go
package stress_test

import (
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func TestConfig_Defaults_Valid(t *testing.T) {
	cfg := stress.Defaults()
	require.NoError(t, cfg.Validate())
	require.Equal(t, "mixed", cfg.Scenario)
	require.Equal(t, "127.0.0.1:5354", cfg.Target)
	require.Equal(t, "127.0.0.1:9090", cfg.Admin)
	require.Equal(t, 60*time.Second, cfg.Duration)
	require.Equal(t, uint32(50), cfg.Concurrency)
	require.True(t, cfg.Spawn)
}

func TestConfig_Validate_BlankScenarioRejected(t *testing.T) {
	cfg := stress.Defaults()
	cfg.Scenario = ""
	require.ErrorContains(t, cfg.Validate(), "scenario")
}

func TestConfig_Validate_ZeroConcurrencyRejected(t *testing.T) {
	cfg := stress.Defaults()
	cfg.Concurrency = 0
	require.ErrorContains(t, cfg.Validate(), "concurrency")
}

func TestConfig_Validate_ZeroDurationRejected(t *testing.T) {
	cfg := stress.Defaults()
	cfg.Duration = 0
	require.ErrorContains(t, cfg.Validate(), "duration")
}

func TestConfig_Validate_BlankTargetRejected(t *testing.T) {
	cfg := stress.Defaults()
	cfg.Target = ""
	require.ErrorContains(t, cfg.Validate(), "target")
}

func TestConfig_Validate_BlankAdminRejected(t *testing.T) {
	cfg := stress.Defaults()
	cfg.Admin = ""
	require.ErrorContains(t, cfg.Validate(), "admin")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stress/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write minimal implementation**

`internal/stress/config.go`:

```go
// Package stress drives BNS stress runs. It composes the imported
// dnspyre library, scrapes BNS's admin endpoints, and renders a report.
package stress

import (
	"errors"
	"fmt"
	"time"
)

// Config holds the inputs to a single stress run.
type Config struct {
	// Scenario name, must be registered.
	Scenario string

	// Target is the BNS DNS listener address dnspyre dials, host:port.
	Target string

	// Admin is the BNS admin HTTP address scraped for /metrics and pprof,
	// host:port. Defaults to host-of-Target:9090.
	Admin string

	// Spawn controls whether the orchestrator starts a local bns + mockupstream.
	// Set false when --target points at an externally-running BNS.
	Spawn bool

	// Duration of load generation.
	Duration time.Duration

	// Concurrency is the dnspyre worker count.
	Concurrency uint32

	// RateLimit applied globally across all dnspyre workers, queries/second.
	// Zero means unbounded.
	RateLimit int

	// BNSBin / MockBin point to the binaries to spawn when Spawn is true.
	BNSBin  string
	MockBin string

	// BlocklistPath supplied to BNS via env. Empty = use example sample.
	BlocklistPath string

	// OutDir is the per-run artefact directory.
	OutDir string

	// PprofCPU duration of the CPU profile capture during the run.
	// Zero disables CPU profile capture.
	PprofCPU time.Duration

	// PprofHeap requests a heap snapshot at end of run.
	PprofHeap bool
}

// Defaults returns a Config populated with the orchestrator's documented
// defaults. The caller must still set scenario-specific overrides and
// Validate() before use.
func Defaults() Config {
	return Config{
		Scenario:    "mixed",
		Target:      "127.0.0.1:5354",
		Admin:       "127.0.0.1:9090",
		Spawn:       true,
		Duration:    60 * time.Second,
		Concurrency: 50,
		RateLimit:   0,
		BNSBin:      "./bin/bns",
		MockBin:     "./bin/mockupstream",
		PprofCPU:    30 * time.Second,
		PprofHeap:   true,
	}
}

// Validate rejects an obviously broken Config. It is not a substitute for
// runtime checks (binary existence, port availability, etc.).
func (c Config) Validate() error {
	var errs []error
	if c.Scenario == "" {
		errs = append(errs, errors.New("scenario must be set"))
	}
	if c.Target == "" {
		errs = append(errs, errors.New("target must be set"))
	}
	if c.Admin == "" {
		errs = append(errs, errors.New("admin must be set"))
	}
	if c.Duration <= 0 {
		errs = append(errs, errors.New("duration must be > 0"))
	}
	if c.Concurrency == 0 {
		errs = append(errs, errors.New("concurrency must be > 0"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config: %w", errors.Join(errs...))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/stress/...`
Expected: PASS for all 6 cases.

- [ ] **Step 5: Commit**

```bash
git add internal/stress/config.go internal/stress/config_test.go
git commit -m "Add stress.Config with defaults and validation

Defines the orchestrator's input surface and the documented defaults
(mixed/60s/50c, target 127.0.0.1:5354, admin :9090).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: stress.Scenario — registry + mixed scenario

**Files:**
- Create: `internal/stress/scenario.go`
- Create: `internal/stress/scenarios/mixed.go`
- Test: `internal/stress/scenarios/mixed_test.go`

- [ ] **Step 1: Write the failing test**

`internal/stress/scenarios/mixed_test.go`:

```go
package scenarios_test

import (
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/bcrisp4/bns/internal/stress/scenarios"
	"github.com/stretchr/testify/require"
)

func TestMixed_BuildPopulatesBenchmark(t *testing.T) {
	s := scenarios.NewMixed()
	require.Equal(t, "mixed", s.Name)

	b := s.Build("127.0.0.1:5354", 30*time.Second, 25)

	require.Equal(t, "127.0.0.1:5354", b.Server)
	require.Equal(t, []string{"A", "AAAA"}, b.Types)
	require.Equal(t, uint32(25), b.Concurrency)
	require.Equal(t, 30*time.Second, b.Duration)
	require.InDelta(t, 0.7, b.Probability, 0.0001)
	require.Equal(t, []string{"@scripts/stress/queries/mixed.txt"}, b.Queries)
	require.True(t, b.Recurse)
	require.True(t, b.Silent)
	require.False(t, b.ProgressBar)
	require.False(t, b.JSON)
	require.True(t, b.Rcodes)
}

func TestRegistry_LookupMixed(t *testing.T) {
	got, ok := stress.LookupScenario("mixed")
	require.True(t, ok)
	require.Equal(t, "mixed", got.Name)
}

func TestRegistry_LookupUnknownReturnsFalse(t *testing.T) {
	_, ok := stress.LookupScenario("does-not-exist")
	require.False(t, ok)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stress/...`
Expected: FAIL — `LookupScenario` and `NewMixed` undefined.

- [ ] **Step 3: Write `scenario.go`**

`internal/stress/scenario.go`:

```go
package stress

import (
	"io"
	"time"

	"github.com/tantalor93/dnspyre/v3/pkg/dnsbench"
)

// Scenario describes a single named stress workload. The Build closure is
// the only place a scenario customises the dnspyre Benchmark; the
// orchestrator fills in fields that are universal (Writer, ErrWriter,
// Silent, ProgressBar) afterwards.
type Scenario struct {
	Name          string
	BlocklistPath string            // optional override; empty = orchestrator default
	BNSEnv        map[string]string // additional BNS_* env vars
	Build         func(target string, dur time.Duration, c uint32) dnsbench.Benchmark
}

// scenarios is the in-memory registry. Populated by package-level
// RegisterScenario calls from sibling packages (e.g. scenarios.NewMixed
// registers via init in its package, but we keep registration explicit
// from the orchestrator for testability).
var scenarios = map[string]Scenario{}

// RegisterScenario adds s to the global registry. Re-registering an
// existing name panics — scenarios are static at startup.
func RegisterScenario(s Scenario) {
	if _, exists := scenarios[s.Name]; exists {
		panic("stress: scenario already registered: " + s.Name)
	}
	scenarios[s.Name] = s
}

// LookupScenario returns the scenario by name and whether it exists.
func LookupScenario(name string) (Scenario, bool) {
	s, ok := scenarios[name]
	return s, ok
}

// stdSilent applies the universal "no stdout, no progress bar" flags.
// Scenarios call it to keep the Build function focused on knobs.
func stdSilent(b *dnsbench.Benchmark) {
	b.Writer = io.Discard
	b.Silent = true
	b.ProgressBar = false
	b.JSON = false
}
```

- [ ] **Step 4: Write `scenarios/mixed.go`**

`internal/stress/scenarios/mixed.go`:

```go
// Package scenarios contains the stress scenarios registered into the
// orchestrator at startup. Today only "mixed" is registered.
package scenarios

import (
	"io"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/tantalor93/dnspyre/v3/pkg/dnsbench"
)

// NewMixed returns the mixed-realistic scenario: ~70% cache-hot, ~20%
// cold, ~10% blocked, A+AAAA per name, probability 0.7 to randomise
// across workers.
func NewMixed() stress.Scenario {
	return stress.Scenario{
		Name: "mixed",
		Build: func(target string, dur time.Duration, c uint32) dnsbench.Benchmark {
			return dnsbench.Benchmark{
				Server:       target,
				Types:        []string{"A", "AAAA"},
				Concurrency:  c,
				Duration:     dur,
				Probability:  0.7,
				Queries:      []string{"@scripts/stress/queries/mixed.txt"},
				Recurse:      true,
				Rcodes:       true,
				HistDisplay:  false,
				HistPre:      dnsbench.DefaultHistPrecision,
				HistMin:      0,
				HistMax:      dnsbench.DefaultRequestTimeout,
				Writer:       io.Discard,
				Silent:       true,
				ProgressBar:  false,
				JSON:         false,
			}
		},
	}
}

func init() {
	stress.RegisterScenario(NewMixed())
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/stress/...`
Expected: PASS. The `scenarios_test` package import side-effect registers `mixed` for the lookup tests.

Note: because the lookup tests live in `scenarios_test` (external test package), the registration `init` from `scenarios` runs when the test binary imports it. If a future test under `internal/stress` itself needs the registry populated, it must add a blank import: `_ "github.com/bcrisp4/bns/internal/stress/scenarios"`.

- [ ] **Step 6: Commit**

```bash
git add internal/stress/scenario.go internal/stress/scenarios/mixed.go internal/stress/scenarios/mixed_test.go
git commit -m "Add stress scenario registry and mixed scenario

Scenario is the pluggable seam: each scenario builds a dnsbench.Benchmark
from (target, duration, concurrency). Today only the mixed scenario is
registered; siblings (cache-hot, cache-cold, blocked-only) are YAGNI.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: stress.Metrics — Prometheus snapshot parse + diff

**Files:**
- Create: `internal/stress/metrics.go`
- Test: `internal/stress/metrics_test.go`
- Create: `internal/stress/testdata/before.prom`
- Create: `internal/stress/testdata/after.prom`

- [ ] **Step 1: Create fixture files**

`internal/stress/testdata/before.prom`:

```
# HELP bns_queries_total Queries by outcome and qtype.
# TYPE bns_queries_total counter
bns_queries_total{outcome="forwarded",qtype="A"} 100
bns_queries_total{outcome="blocked",qtype="A"} 10
bns_queries_total{outcome="nxdomain",qtype="A"} 2
bns_queries_total{outcome="error",qtype="A"} 0
# HELP bns_upstream_queries_total Upstream queries.
# TYPE bns_upstream_queries_total counter
bns_upstream_queries_total 95
# HELP bns_coalesced_queries_total Coalesced queries piggybacked on in-flight upstream call.
# TYPE bns_coalesced_queries_total counter
bns_coalesced_queries_total 5
# HELP bns_cache_evictions_total LRU evictions.
# TYPE bns_cache_evictions_total counter
bns_cache_evictions_total 0
# HELP bns_panics_total Panics recovered in the resolver chain.
# TYPE bns_panics_total counter
bns_panics_total 0
# HELP bns_query_duration_seconds Per-query duration.
# TYPE bns_query_duration_seconds histogram
bns_query_duration_seconds_bucket{le="0.0005"} 80
bns_query_duration_seconds_bucket{le="0.001"} 100
bns_query_duration_seconds_bucket{le="0.002"} 110
bns_query_duration_seconds_bucket{le="0.005"} 112
bns_query_duration_seconds_bucket{le="+Inf"} 112
bns_query_duration_seconds_sum 0.05
bns_query_duration_seconds_count 112
```

`internal/stress/testdata/after.prom`:

```
# HELP bns_queries_total Queries by outcome and qtype.
# TYPE bns_queries_total counter
bns_queries_total{outcome="forwarded",qtype="A"} 1100
bns_queries_total{outcome="blocked",qtype="A"} 210
bns_queries_total{outcome="nxdomain",qtype="A"} 12
bns_queries_total{outcome="error",qtype="A"} 1
# HELP bns_upstream_queries_total Upstream queries.
# TYPE bns_upstream_queries_total counter
bns_upstream_queries_total 395
# HELP bns_coalesced_queries_total Coalesced queries piggybacked on in-flight upstream call.
# TYPE bns_coalesced_queries_total counter
bns_coalesced_queries_total 55
# HELP bns_cache_evictions_total LRU evictions.
# TYPE bns_cache_evictions_total counter
bns_cache_evictions_total 3
# HELP bns_panics_total Panics recovered in the resolver chain.
# TYPE bns_panics_total counter
bns_panics_total 0
# HELP bns_query_duration_seconds Per-query duration.
# TYPE bns_query_duration_seconds histogram
bns_query_duration_seconds_bucket{le="0.0005"} 900
bns_query_duration_seconds_bucket{le="0.001"} 1200
bns_query_duration_seconds_bucket{le="0.002"} 1300
bns_query_duration_seconds_bucket{le="0.005"} 1322
bns_query_duration_seconds_bucket{le="+Inf"} 1323
bns_query_duration_seconds_sum 0.95
bns_query_duration_seconds_count 1323
```

- [ ] **Step 2: Write the failing test**

`internal/stress/metrics_test.go`:

```go
package stress_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func TestSnapshot_Parse(t *testing.T) {
	snap, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)

	require.Equal(t, int64(100), snap.QueriesByOutcome["forwarded"])
	require.Equal(t, int64(10), snap.QueriesByOutcome["blocked"])
	require.Equal(t, int64(95), snap.UpstreamQueries)
	require.Equal(t, int64(5), snap.CoalescedQueries)
	require.Equal(t, int64(0), snap.CacheEvictions)
	require.Equal(t, int64(0), snap.Panics)
	require.Equal(t, int64(112), snap.DurationHist.Count)
}

func TestSnapshot_Diff(t *testing.T) {
	before, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)
	after, err := stress.ParseSnapshot(readFixture(t, "after.prom"))
	require.NoError(t, err)

	d := stress.Diff(before, after)

	require.Equal(t, int64(1000), d.QueriesByOutcome["forwarded"])
	require.Equal(t, int64(200), d.QueriesByOutcome["blocked"])
	require.Equal(t, int64(10), d.QueriesByOutcome["nxdomain"])
	require.Equal(t, int64(1), d.QueriesByOutcome["error"])
	require.Equal(t, int64(1211), d.TotalQueries)
	require.Equal(t, int64(300), d.UpstreamQueries)
	require.Equal(t, int64(50), d.CoalescedQueries)
	require.Equal(t, int64(3), d.CacheEvictions)
	require.Equal(t, int64(0), d.Panics)

	// Cache hit rate = 1 - upstream/total. With these numbers: 1 - 300/1211 = 0.7522
	require.InDelta(t, 0.7522, d.CacheHitRate(), 0.001)

	// p50 falls in the (0.0005, 0.001] bucket given the delta histogram:
	// before bucket counts: 80, 100, 110, 112 (cumulative). After: 900, 1200, 1300, 1322.
	// Delta cumulative: 820, 1100, 1190, 1210. p50 of 1211 -> 605.5 in bucket index 0 (le=0.0005, count 820).
	p50 := d.DurationQuantile(0.50)
	require.GreaterOrEqual(t, p50.Seconds(), 0.0)
	require.LessOrEqual(t, p50.Seconds(), 0.0005)

	// p99 should sit in or beyond the (0.001, 0.002] bucket.
	p99 := d.DurationQuantile(0.99)
	require.GreaterOrEqual(t, p99.Seconds(), 0.0005)
}

func TestSnapshot_DiffZeroAcrossEqualSnapshots(t *testing.T) {
	snap, err := stress.ParseSnapshot(readFixture(t, "before.prom"))
	require.NoError(t, err)
	d := stress.Diff(snap, snap)
	require.Equal(t, int64(0), d.TotalQueries)
	require.Equal(t, int64(0), d.UpstreamQueries)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/stress/... -run Snapshot -v`
Expected: FAIL — `ParseSnapshot`, `Diff`, etc. undefined.

- [ ] **Step 4: Write implementation**

`internal/stress/metrics.go`:

```go
package stress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HistogramBucket is one cumulative bucket from a Prometheus histogram.
type HistogramBucket struct {
	UpperBound float64 // le="" value; +Inf as math.Inf(+1)
	Count      int64
}

// HistogramSnapshot captures the parts of a Prometheus histogram needed
// for quantile interpolation.
type HistogramSnapshot struct {
	Buckets []HistogramBucket // sorted by UpperBound
	Sum     float64
	Count   int64
}

// Snapshot holds the BNS counters and the query duration histogram at a
// single point in time. Only the metrics referenced in the report are
// extracted — extra metrics in the input are silently ignored.
type Snapshot struct {
	QueriesByOutcome map[string]int64 // sum across qtypes per outcome label
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64
	DurationHist     HistogramSnapshot
}

// MetricsDiff is the delta of two Snapshots, with derived quantities.
type MetricsDiff struct {
	QueriesByOutcome map[string]int64
	TotalQueries     int64
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64
	DurationHist     HistogramSnapshot // delta bucket counts + delta sum/count
}

// CacheHitRate returns 1 - upstream/total. Returns 0 when total is zero.
func (d MetricsDiff) CacheHitRate() float64 {
	if d.TotalQueries == 0 {
		return 0
	}
	return 1.0 - float64(d.UpstreamQueries)/float64(d.TotalQueries)
}

// DurationQuantile interpolates a quantile from the diff's histogram. q
// is in [0, 1]. Returns 0 when the histogram is empty.
func (d MetricsDiff) DurationQuantile(q float64) time.Duration {
	h := d.DurationHist
	if h.Count == 0 {
		return 0
	}
	target := float64(h.Count) * q
	var prevBound float64
	var prevCount int64
	for _, b := range h.Buckets {
		if float64(b.Count) >= target {
			width := b.UpperBound - prevBound
			countIn := float64(b.Count - prevCount)
			if countIn <= 0 {
				return time.Duration(b.UpperBound * float64(time.Second))
			}
			frac := (target - float64(prevCount)) / countIn
			seconds := prevBound + frac*width
			return time.Duration(seconds * float64(time.Second))
		}
		prevBound = b.UpperBound
		prevCount = b.Count
	}
	return time.Duration(h.Buckets[len(h.Buckets)-1].UpperBound * float64(time.Second))
}

// ParseSnapshot reads a Prometheus text-exposition body and extracts the
// fields the stress harness cares about.
func ParseSnapshot(raw []byte) (Snapshot, error) {
	snap := Snapshot{QueriesByOutcome: map[string]int64{}}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, err := parsePromLine(line)
		if err != nil {
			return snap, err
		}
		switch name {
		case "bns_queries_total":
			outcome := labels["outcome"]
			if outcome == "" {
				continue
			}
			snap.QueriesByOutcome[outcome] += int64(value)
		case "bns_upstream_queries_total":
			snap.UpstreamQueries += int64(value)
		case "bns_coalesced_queries_total":
			snap.CoalescedQueries += int64(value)
		case "bns_cache_evictions_total":
			snap.CacheEvictions += int64(value)
		case "bns_panics_total":
			snap.Panics += int64(value)
		case "bns_query_duration_seconds_bucket":
			ub := labels["le"]
			b, err := parseLE(ub)
			if err != nil {
				return snap, err
			}
			snap.DurationHist.Buckets = append(snap.DurationHist.Buckets,
				HistogramBucket{UpperBound: b, Count: int64(value)})
		case "bns_query_duration_seconds_sum":
			snap.DurationHist.Sum = value
		case "bns_query_duration_seconds_count":
			snap.DurationHist.Count = int64(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return snap, err
	}

	// Sort histogram buckets by upper bound; the prometheus text format
	// is supposed to be sorted but we don't rely on it.
	for i := 1; i < len(snap.DurationHist.Buckets); i++ {
		for j := i; j > 0 && snap.DurationHist.Buckets[j-1].UpperBound > snap.DurationHist.Buckets[j].UpperBound; j-- {
			snap.DurationHist.Buckets[j-1], snap.DurationHist.Buckets[j] = snap.DurationHist.Buckets[j], snap.DurationHist.Buckets[j-1]
		}
	}
	return snap, nil
}

// FetchSnapshot scrapes <baseURL>/metrics and returns the parsed Snapshot.
// baseURL has the form "http://host:port".
func FetchSnapshot(client *http.Client, baseURL string) (Snapshot, []byte, error) {
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, body, fmt.Errorf("metrics status %d", resp.StatusCode)
	}
	snap, err := ParseSnapshot(body)
	return snap, body, err
}

// Diff returns after - before for every recorded counter.
func Diff(before, after Snapshot) MetricsDiff {
	d := MetricsDiff{QueriesByOutcome: map[string]int64{}}
	for k, v := range after.QueriesByOutcome {
		d.QueriesByOutcome[k] = v - before.QueriesByOutcome[k]
		d.TotalQueries += d.QueriesByOutcome[k]
	}
	d.UpstreamQueries = after.UpstreamQueries - before.UpstreamQueries
	d.CoalescedQueries = after.CoalescedQueries - before.CoalescedQueries
	d.CacheEvictions = after.CacheEvictions - before.CacheEvictions
	d.Panics = after.Panics - before.Panics

	d.DurationHist = diffHistogram(before.DurationHist, after.DurationHist)
	return d
}

func diffHistogram(before, after HistogramSnapshot) HistogramSnapshot {
	if len(before.Buckets) != len(after.Buckets) {
		return HistogramSnapshot{
			Buckets: append([]HistogramBucket(nil), after.Buckets...),
			Sum:     after.Sum - before.Sum,
			Count:   after.Count - before.Count,
		}
	}
	out := HistogramSnapshot{
		Buckets: make([]HistogramBucket, len(after.Buckets)),
		Sum:     after.Sum - before.Sum,
		Count:   after.Count - before.Count,
	}
	for i := range after.Buckets {
		out.Buckets[i] = HistogramBucket{
			UpperBound: after.Buckets[i].UpperBound,
			Count:      after.Buckets[i].Count - before.Buckets[i].Count,
		}
	}
	return out
}

func parsePromLine(line string) (name string, labels map[string]string, value float64, err error) {
	labels = map[string]string{}
	openBrace := strings.IndexByte(line, '{')
	spaceIdx := strings.LastIndexByte(line, ' ')
	if spaceIdx < 0 {
		return "", nil, 0, fmt.Errorf("malformed line: %q", line)
	}
	valueStr := line[spaceIdx+1:]
	v, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("bad value in %q: %w", line, err)
	}
	value = v

	if openBrace == -1 {
		name = line[:spaceIdx]
		return name, labels, value, nil
	}
	name = line[:openBrace]
	closeBrace := strings.IndexByte(line, '}')
	if closeBrace == -1 {
		return "", nil, 0, fmt.Errorf("missing } in %q", line)
	}
	labelBody := line[openBrace+1 : closeBrace]
	for _, pair := range splitLabels(labelBody) {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(pair[:eq])
		val := strings.Trim(strings.TrimSpace(pair[eq+1:]), "\"")
		labels[key] = val
	}
	return name, labels, value, nil
}

// splitLabels splits a Prometheus label block on commas that are not
// inside a quoted value. The exposition format does not allow embedded
// quotes other than \", so this is sufficient for BNS's metrics.
func splitLabels(body string) []string {
	var out []string
	depth := 0
	last := 0
	for i, ch := range body {
		switch ch {
		case '"':
			depth ^= 1
		case ',':
			if depth == 0 {
				out = append(out, body[last:i])
				last = i + 1
			}
		}
	}
	if last < len(body) {
		out = append(out, body[last:])
	}
	return out
}

func parseLE(s string) (float64, error) {
	if s == "+Inf" {
		return math.Inf(+1), nil
	}
	return strconv.ParseFloat(s, 64)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/stress/... -run Snapshot -v`
Expected: PASS for all three cases.

- [ ] **Step 6: Commit**

```bash
git add internal/stress/metrics.go internal/stress/metrics_test.go internal/stress/testdata/before.prom internal/stress/testdata/after.prom
git commit -m "Add Prometheus snapshot parser and diff helper

ParseSnapshot extracts only the BNS counters and the query duration
histogram. Diff computes deltas + CacheHitRate + DurationQuantile via
linear bucket interpolation. Fixture-driven tests cover the diff math.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: stress.Report — render Markdown report

**Files:**
- Create: `internal/stress/report.go`
- Test: `internal/stress/report_test.go`
- Create: `internal/stress/testdata/report_golden.md`

- [ ] **Step 1: Write the failing test**

`internal/stress/report_test.go`:

```go
package stress_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func TestReport_RenderGolden(t *testing.T) {
	input := stress.ReportInput{
		Scenario:     "mixed",
		StartedAt:    time.Date(2026, 5, 19, 14, 22, 3, 0, time.UTC),
		Target:       "127.0.0.1:5354",
		Admin:        "127.0.0.1:9090",
		BNSGitSha:    "abc1234",
		GoVersion:    "go1.26.3",
		Host:         "linux/amd64 6.17.0-23-generic",
		Duration:     60 * time.Second,
		Concurrency:  50,
		TotalQueries: 1211,
		QPS:          20.18,
		P50:          400 * time.Microsecond,
		P95:          1100 * time.Microsecond,
		P99:          1800 * time.Microsecond,
		IOErrors:     0,
		IDMismatches: 0,
		Counters: map[string]int64{
			"forwarded": 1000,
			"blocked":   200,
			"nxdomain":  10,
			"error":     1,
		},
		UpstreamQueries:  300,
		CoalescedQueries: 50,
		CacheEvictions:   3,
		Panics:           0,
		PprofCPU:         "cpu.pprof",
		PprofHeap:        "heap.pprof",
	}

	got := stress.Render(input)

	wantBytes, err := os.ReadFile(filepath.Join("testdata", "report_golden.md"))
	require.NoError(t, err)
	require.Equal(t, string(wantBytes), got)
}
```

- [ ] **Step 2: Create the golden fixture**

`internal/stress/testdata/report_golden.md`:

```markdown
# bns stress report — mixed — 2026-05-19T14:22:03Z

## Setup
- target: 127.0.0.1:5354
- admin: 127.0.0.1:9090
- bns: abc1234
- go: go1.26.3
- host: linux/amd64 6.17.0-23-generic
- scenario: mixed | duration: 1m0s | concurrency: 50

## Headline
| metric        | value      |
|---------------|------------|
| sustained QPS | 20         |
| p50 / p95 / p99 (client-side) | 0.4 / 1.1 / 1.8 ms |
| total queries | 1,211      |
| io errors     | 0          |
| id mismatches | 0          |

## Outcome breakdown (bns perspective, after − before)
| outcome    | count    | %      |
|------------|----------|--------|
| forwarded  | 1,000    | 82.6   |
| blocked    | 200      | 16.5   |
| nxdomain   | 10       | 0.8    |
| error      | 1        | 0.1    |

## Internals
- upstream queries: 300   (cache hit rate = 75.2%)
- coalesced queries: 50
- cache evictions: 3
- panics: 0

## Profiles
- CPU: cpu.pprof   — `go tool pprof -top dist/stress/<this-dir>/cpu.pprof`
- Heap: heap.pprof — `go tool pprof -top dist/stress/<this-dir>/heap.pprof`
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/stress/... -run Report -v`
Expected: FAIL — `Render` and `ReportInput` undefined.

- [ ] **Step 4: Write implementation**

`internal/stress/report.go`:

```go
package stress

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReportInput is the fully-resolved set of values used to render the
// per-run report.md. All formatting decisions (rounding, units, etc.)
// happen inside Render.
type ReportInput struct {
	Scenario     string
	StartedAt    time.Time
	Target       string
	Admin        string
	BNSGitSha    string
	GoVersion    string
	Host         string
	Duration     time.Duration
	Concurrency  uint32

	// Client-side numbers from dnspyre.
	TotalQueries int64
	QPS          float64
	P50          time.Duration
	P95          time.Duration
	P99          time.Duration
	IOErrors     int64
	IDMismatches int64

	// BNS-side counters (deltas from metrics scrape).
	Counters         map[string]int64
	UpstreamQueries  int64
	CoalescedQueries int64
	CacheEvictions   int64
	Panics           int64

	// Pprof artefact filenames (relative to OutDir). Empty when skipped.
	PprofCPU  string
	PprofHeap string
}

// Render produces the human-readable report.md. The output is stable for
// a given input — used both for the on-disk artefact and as a fixture in
// tests.
func Render(in ReportInput) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# bns stress report — %s — %s\n", in.Scenario, in.StartedAt.UTC().Format("2006-01-02T15:04:05Z"))
	sb.WriteString("\n## Setup\n")
	fmt.Fprintf(&sb, "- target: %s\n", in.Target)
	fmt.Fprintf(&sb, "- admin: %s\n", in.Admin)
	fmt.Fprintf(&sb, "- bns: %s\n", in.BNSGitSha)
	fmt.Fprintf(&sb, "- go: %s\n", in.GoVersion)
	fmt.Fprintf(&sb, "- host: %s\n", in.Host)
	fmt.Fprintf(&sb, "- scenario: %s | duration: %s | concurrency: %d\n", in.Scenario, in.Duration, in.Concurrency)

	sb.WriteString("\n## Headline\n")
	sb.WriteString("| metric        | value      |\n")
	sb.WriteString("|---------------|------------|\n")
	fmt.Fprintf(&sb, "| sustained QPS | %-10s |\n", formatInt(int64(in.QPS)))
	fmt.Fprintf(&sb, "| p50 / p95 / p99 (client-side) | %s / %s / %s ms |\n",
		formatMillis(in.P50), formatMillis(in.P95), formatMillis(in.P99))
	fmt.Fprintf(&sb, "| total queries | %-10s |\n", formatInt(in.TotalQueries))
	fmt.Fprintf(&sb, "| io errors     | %-10d |\n", in.IOErrors)
	fmt.Fprintf(&sb, "| id mismatches | %-10d |\n", in.IDMismatches)

	sb.WriteString("\n## Outcome breakdown (bns perspective, after − before)\n")
	sb.WriteString("| outcome    | count    | %      |\n")
	sb.WriteString("|------------|----------|--------|\n")

	total := int64(0)
	for _, v := range in.Counters {
		total += v
	}
	keys := make([]string, 0, len(in.Counters))
	for k := range in.Counters {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return outcomeOrder(keys[i]) < outcomeOrder(keys[j])
	})
	for _, k := range keys {
		v := in.Counters[k]
		pct := 0.0
		if total > 0 {
			pct = 100.0 * float64(v) / float64(total)
		}
		fmt.Fprintf(&sb, "| %-10s | %-8s | %-6s |\n", k, formatInt(v), formatPct(pct))
	}

	sb.WriteString("\n## Internals\n")
	hitRate := 0.0
	if in.TotalQueries > 0 {
		hitRate = 100.0 * (1.0 - float64(in.UpstreamQueries)/float64(in.TotalQueries))
	}
	fmt.Fprintf(&sb, "- upstream queries: %s   (cache hit rate = %s%%)\n", formatInt(in.UpstreamQueries), formatPct(hitRate))
	fmt.Fprintf(&sb, "- coalesced queries: %s\n", formatInt(in.CoalescedQueries))
	fmt.Fprintf(&sb, "- cache evictions: %s\n", formatInt(in.CacheEvictions))
	fmt.Fprintf(&sb, "- panics: %s\n", formatInt(in.Panics))

	if in.PprofCPU != "" || in.PprofHeap != "" {
		sb.WriteString("\n## Profiles\n")
		if in.PprofCPU != "" {
			fmt.Fprintf(&sb, "- CPU: %s   — `go tool pprof -top dist/stress/<this-dir>/%s`\n", in.PprofCPU, in.PprofCPU)
		}
		if in.PprofHeap != "" {
			fmt.Fprintf(&sb, "- Heap: %s — `go tool pprof -top dist/stress/<this-dir>/%s`\n", in.PprofHeap, in.PprofHeap)
		}
	}

	return sb.String()
}

func outcomeOrder(name string) int {
	switch name {
	case "forwarded":
		return 0
	case "blocked":
		return 1
	case "nxdomain":
		return 2
	case "error":
		return 3
	default:
		return 99
	}
}

// formatInt renders an int64 with thousands separators.
func formatInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 && n > -1000 {
		return s
	}
	// Insert commas right-to-left.
	neg := false
	if s[0] == '-' {
		neg = true
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// formatPct renders a percentage to one decimal place.
func formatPct(p float64) string {
	return fmt.Sprintf("%.1f", p)
}

// formatMillis renders a duration in ms to one decimal place.
func formatMillis(d time.Duration) string {
	return fmt.Sprintf("%.1f", float64(d)/float64(time.Millisecond))
}
```

- [ ] **Step 5: Run test, fix golden if needed**

Run: `go test ./internal/stress/... -run Report -v`
Expected: PASS. If the formatting bytes differ, examine the diff carefully — the golden file is the source of truth; update the renderer to match if the renderer is wrong, or update the golden if the renderer is correct.

- [ ] **Step 6: Commit**

```bash
git add internal/stress/report.go internal/stress/report_test.go internal/stress/testdata/report_golden.md
git commit -m "Add stress report renderer with golden test

Render produces a stable Markdown report from a ReportInput value. The
golden fixture captures the formatting contract; renderer changes that
shift bytes will fail the test loudly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Extend admin server with optional pprof

**Files:**
- Modify: `internal/admin/server.go`
- Modify: `internal/admin/server_test.go`
- Modify: `cmd/bns/serve.go`

- [ ] **Step 1: Inspect existing admin test file**

Run: `cat internal/admin/server_test.go`
Expected: gives the existing testing pattern (routes + listener + handlers).

- [ ] **Step 2: Write a failing test for the pprof case**

Append to `internal/admin/server_test.go`:

```go
func TestServer_PprofEnabledExposesProfileEndpoints(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	rdy := health.NewReadiness()
	rdy.Set("dummy", true)
	reg := prometheus.NewRegistry()

	srv := admin.New(ln, reg, rdy, admin.WithPprof())
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// /debug/pprof/heap should return a non-empty body and Content-Type
	// matching pprof binary output.
	resp, err := http.Get("http://" + addr + "/debug/pprof/heap")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, body)
}

func TestServer_PprofDisabledByDefault(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	rdy := health.NewReadiness()
	rdy.Set("dummy", true)
	reg := prometheus.NewRegistry()

	srv := admin.New(ln, reg, rdy)
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	resp, err := http.Get("http://" + addr + "/debug/pprof/heap")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
```

Make sure the imports at the top of `server_test.go` include `context`, `io`, `net`, `net/http`, the stdlib `testing`, `prometheus`, `health`, and `admin`. Add missing imports.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/admin/... -run Pprof -v`
Expected: FAIL — `admin.WithPprof` does not exist; `admin.New` signature does not accept variadic options.

- [ ] **Step 4: Add the option to `internal/admin/server.go`**

Replace the existing file with:

```go
// Package admin hosts the BNS administrative HTTP endpoints:
// /metrics (Prometheus), /healthz (liveness), /readyz (readiness),
// and optionally /debug/pprof/* when WithPprof is passed.
package admin

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/bcrisp4/bns/internal/health"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server bundles the admin HTTP server with its mux and listener.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// Option mutates the admin mux at construction time.
type Option func(mux *http.ServeMux)

// WithPprof mounts net/http/pprof handlers under /debug/pprof/. Off by
// default; the BNS serve command opts in via the --pprof flag.
func WithPprof() Option {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
}

// New builds an admin server bound to ln, exposing metrics from reg and
// health from rdy.
func New(ln net.Listener, reg prometheus.Gatherer, rdy *health.Readiness, opts ...Option) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler(rdy))
	for _, o := range opts {
		o(mux)
	}
	return &Server{
		http: &http.Server{Handler: mux},
		ln:   ln,
	}
}

// Serve blocks until the server stops; returns the error from http.Serve.
// http.ErrServerClosed is the normal shutdown signal.
func (s *Server) Serve() error {
	return s.http.Serve(s.ln)
}

// Shutdown stops accepting connections and waits for in-flight requests
// to complete (bounded by ctx).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
```

- [ ] **Step 5: Run admin tests to verify they pass**

Run: `go test ./internal/admin/...`
Expected: PASS for all existing tests plus the two new pprof tests.

- [ ] **Step 6: Add `--pprof` flag to `bns serve`**

In `cmd/bns/serve.go`, near the existing `cmd.Flags()` calls (around line 49):

```go
	cmd.Flags().Bool("pprof", false, "Expose /debug/pprof endpoints on the admin listener (default off)")
```

Bind via viper in `bindServeFlags`:

```go
	if err := v.BindPFlag("admin.pprof", c.Flag("pprof")); err != nil {
		return err
	}
```

In the admin server construction site (around line 138, look for `adminSrv := admin.New(...)`), append the option conditionally:

```go
	var adminOpts []admin.Option
	if cfg.Admin.Pprof {
		adminOpts = append(adminOpts, admin.WithPprof())
	}
	adminSrv := admin.New(adminLn, reg, rdy, adminOpts...)
```

- [ ] **Step 7: Add `Pprof bool` to the admin section of the config struct**

Find `internal/config/load.go` (or whichever file defines the `Admin` struct). Add a `Pprof bool \`mapstructure:"pprof"\`` field. Search to confirm:

Run: `grep -nE "type Admin\b|Admin struct" internal/config/*.go`
Expected: a single struct definition. Add the field, preserving existing struct tags. If the field is unset by config it defaults to `false`.

If the struct lives in a single file you read briefly, the addition is one line. Otherwise, locate it before adding.

- [ ] **Step 8: Run all unit tests to verify nothing broke**

Run: `make test`
Expected: PASS.

- [ ] **Step 9: Run race detector**

Run: `make race`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/admin/server.go internal/admin/server_test.go cmd/bns/serve.go internal/config/load.go
git commit -m "Add optional --pprof flag to bns serve

Extends admin.New with a variadic Option pattern. admin.WithPprof mounts
net/http/pprof under /debug/pprof/. The --pprof flag on serve threads the
option through (default off), matching the existing opt-in posture for
debug surfaces.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: stress.Orchestrator — subprocess lifecycle helpers

**Files:**
- Create: `internal/stress/orchestrator.go`
- Test: `internal/stress/orchestrator_test.go`

This task covers the unit-testable parts of the orchestrator: URL builders, env composition, ready-wait HTTP polling against a fake. The full Run sequence is exercised by the integration test in Task 11.

- [ ] **Step 1: Write failing tests**

`internal/stress/orchestrator_test.go`:

```go
package stress_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func TestAdminBaseURL_PrefixesScheme(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:9090", stress.AdminBaseURL("127.0.0.1:9090"))
	require.Equal(t, "http://pi.example:9090", stress.AdminBaseURL("pi.example:9090"))
}

func TestWaitForReady_SuccessAfterPolling(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) < 3 {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, stress.WaitForReady(ctx, srv.URL, 25*time.Millisecond))
	require.GreaterOrEqual(t, hits.Load(), int32(3))
}

func TestWaitForReady_FailsOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := stress.WaitForReady(ctx, srv.URL, 25*time.Millisecond)
	require.Error(t, err)
}

func TestBNSEnv_MergesScenarioAndDefaults(t *testing.T) {
	env := stress.BNSEnv(map[string]string{
		"BNS_LOGGING__LEVEL":          "warn",
		"BNS_BLOCKLISTS__SOURCES__0__PATH": "/tmp/list.txt",
	}, map[string]string{
		"BNS_CACHE__CAPACITY": "10000",
	})

	require.Contains(t, env, "BNS_LOGGING__LEVEL=warn")
	require.Contains(t, env, "BNS_CACHE__CAPACITY=10000")
	require.Contains(t, env, "BNS_BLOCKLISTS__SOURCES__0__PATH=/tmp/list.txt")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stress/... -run "AdminBaseURL|WaitForReady|BNSEnv" -v`
Expected: FAIL — none of `AdminBaseURL`, `WaitForReady`, `BNSEnv` exist.

- [ ] **Step 3: Write the helpers in `orchestrator.go`**

Create `internal/stress/orchestrator.go` with just the helpers needed for the failing tests; the full Run sequence comes in the next step.

```go
package stress

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// AdminBaseURL returns "http://" + admin host:port. The orchestrator
// passes this into FetchSnapshot and the pprof helpers.
func AdminBaseURL(adminHostPort string) string {
	return "http://" + adminHostPort
}

// WaitForReady polls <baseURL>/readyz every interval until it returns
// HTTP 200, or ctx is cancelled / deadline exceeded.
func WaitForReady(ctx context.Context, baseURL string, interval time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("readyz not OK before deadline: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// BNSEnv merges defaults with scenario overrides, returning a sorted
// slice of "KEY=value" strings ready for exec.Cmd.Env.
func BNSEnv(scenario, defaults map[string]string) []string {
	merged := map[string]string{}
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range scenario {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/stress/... -run "AdminBaseURL|WaitForReady|BNSEnv" -v`
Expected: PASS for all four cases.

- [ ] **Step 5: Commit**

```bash
git add internal/stress/orchestrator.go internal/stress/orchestrator_test.go
git commit -m "Add stress orchestrator helpers: WaitForReady, BNSEnv, AdminBaseURL

Pure helpers exercised by unit tests. The full Run sequence (subprocess
lifecycle, pprof capture) lands in the next commit and is covered by the
stress_integration build-tagged test.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: stress.Orchestrator — full Run sequence

**Files:**
- Modify: `internal/stress/orchestrator.go`

This task adds the body of `Run(ctx, cfg)`. The function is exercised end-to-end by Task 11's integration test.

- [ ] **Step 1: Append `Run` and its helpers to `orchestrator.go`**

Append to `internal/stress/orchestrator.go`:

```go
// Result is the outcome of a single Run.
type Result struct {
	OutDir       string
	TotalQueries int64
	QPS          float64
	P50, P95, P99 time.Duration
	IOErrors     int64
	IDMismatches int64
	Counters     map[string]int64
	UpstreamQueries int64
	CoalescedQueries int64
	CacheEvictions int64
	Panics int64
}

// Run executes one stress run end-to-end. The returned Result mirrors
// the headline numbers in report.md, suitable for printing the one-line
// summary.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	scenario, ok := LookupScenario(cfg.Scenario)
	if !ok {
		return Result{}, fmt.Errorf("unknown scenario %q", cfg.Scenario)
	}

	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join("dist", "stress", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create out dir: %w", err)
	}

	// Open log files for child processes (always — even when --no-spawn we
	// keep the path so subsequent Diff failures get logged somewhere).
	bnsLog, err := os.Create(filepath.Join(cfg.OutDir, "bns.log"))
	if err != nil {
		return Result{}, err
	}
	defer bnsLog.Close()
	mockLog, err := os.Create(filepath.Join(cfg.OutDir, "mockupstream.log"))
	if err != nil {
		return Result{}, err
	}
	defer mockLog.Close()

	var bnsCmd, mockCmd *exec.Cmd
	if cfg.Spawn {
		mockAddr := "127.0.0.1:5355"
		mockCmd = exec.CommandContext(ctx, cfg.MockBin,
			"--listen.udp", mockAddr,
			"--listen.tcp", mockAddr,
		)
		mockCmd.Stdout = mockLog
		mockCmd.Stderr = mockLog
		if err := mockCmd.Start(); err != nil {
			return Result{}, fmt.Errorf("start mockupstream: %w", err)
		}
		defer terminate(mockCmd)

		bnsEnv := BNSEnv(scenario.BNSEnv, map[string]string{
			"BNS_LOGGING__LEVEL":               "info",
			"BNS_LOGGING__FORMAT":              "json",
			"BNS_LOGGING__QUERY_LOG__ENABLED":  "false",
			"BNS_CACHE__CAPACITY":              "10000",
		})
		if cfg.BlocklistPath != "" {
			bnsEnv = append(bnsEnv,
				"BNS_BLOCKLISTS__SOURCES__0__TYPE=file",
				"BNS_BLOCKLISTS__SOURCES__0__PATH="+cfg.BlocklistPath,
			)
		}
		bnsCmd = exec.CommandContext(ctx, cfg.BNSBin, "serve",
			"--listen.udp", cfg.Target,
			"--listen.tcp", cfg.Target,
			"--upstream", mockAddr,
			"--pprof",
		)
		bnsCmd.Env = append(os.Environ(), bnsEnv...)
		bnsCmd.Stdout = bnsLog
		bnsCmd.Stderr = bnsLog
		if err := bnsCmd.Start(); err != nil {
			return Result{}, fmt.Errorf("start bns: %w", err)
		}
		defer terminate(bnsCmd)
	}

	baseURL := AdminBaseURL(cfg.Admin)
	readyCtx, readyCancel := context.WithTimeout(ctx, 10*time.Second)
	defer readyCancel()
	if err := WaitForReady(readyCtx, baseURL, 50*time.Millisecond); err != nil {
		return Result{}, fmt.Errorf("bns not ready: %w", err)
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	before, beforeRaw, err := FetchSnapshot(httpClient, baseURL)
	if err != nil {
		return Result{}, fmt.Errorf("scrape before: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "before.prom"), beforeRaw, 0o644); err != nil {
		return Result{}, err
	}

	// Pprof CPU profile runs in parallel with the benchmark.
	pprofErr := make(chan error, 1)
	if cfg.PprofCPU > 0 {
		go func() {
			pprofErr <- capturePprof(ctx, httpClient,
				fmt.Sprintf("%s/debug/pprof/profile?seconds=%d", baseURL, int(cfg.PprofCPU.Seconds())),
				filepath.Join(cfg.OutDir, "cpu.pprof"))
		}()
	} else {
		pprofErr <- nil
	}

	start := time.Now()
	b := scenario.Build(cfg.Target, cfg.Duration, cfg.Concurrency)
	stdSilent(&b)
	if cfg.RateLimit > 0 {
		b.Rate = cfg.RateLimit
	}
	results, err := b.Run(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("dnspyre run: %w", err)
	}
	elapsed := time.Since(start)

	if err := <-pprofErr; err != nil {
		// pprof failure is non-fatal but logged into the run dir.
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "cpu.pprof.err"), []byte(err.Error()), 0o644)
	}

	if cfg.PprofHeap {
		if err := capturePprof(ctx, httpClient,
			baseURL+"/debug/pprof/heap",
			filepath.Join(cfg.OutDir, "heap.pprof")); err != nil {
			_ = os.WriteFile(filepath.Join(cfg.OutDir, "heap.pprof.err"), []byte(err.Error()), 0o644)
		}
	}

	after, afterRaw, err := FetchSnapshot(httpClient, baseURL)
	if err != nil {
		return Result{}, fmt.Errorf("scrape after: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "after.prom"), afterRaw, 0o644); err != nil {
		return Result{}, err
	}

	d := Diff(before, after)
	merged := reporter.Merge(&b, results)

	// Re-render dnspyre's own JSON for archival.
	jsonPath := filepath.Join(cfg.OutDir, "dnspyre-results.json")
	jsonFile, err := os.Create(jsonPath)
	if err == nil {
		bArchival := b
		bArchival.JSON = true
		bArchival.Silent = false
		bArchival.Writer = jsonFile
		_ = reporter.PrintReport(&bArchival, results, start, elapsed)
		jsonFile.Close()
	}

	qps := float64(merged.Counters.Total) / elapsed.Seconds()

	res := Result{
		OutDir:           cfg.OutDir,
		TotalQueries:     merged.Counters.Total,
		QPS:              qps,
		P50:              time.Duration(merged.Hist.ValueAtQuantile(50)),
		P95:              time.Duration(merged.Hist.ValueAtQuantile(95)),
		P99:              time.Duration(merged.Hist.ValueAtQuantile(99)),
		IOErrors:         merged.Counters.IOError,
		IDMismatches:     merged.Counters.IDmismatch,
		Counters:         d.QueriesByOutcome,
		UpstreamQueries:  d.UpstreamQueries,
		CoalescedQueries: d.CoalescedQueries,
		CacheEvictions:   d.CacheEvictions,
		Panics:           d.Panics,
	}

	report := Render(ReportInput{
		Scenario:         cfg.Scenario,
		StartedAt:        start,
		Target:           cfg.Target,
		Admin:            cfg.Admin,
		BNSGitSha:        gitSha(),
		GoVersion:        runtime.Version(),
		Host:             hostString(),
		Duration:         cfg.Duration,
		Concurrency:      cfg.Concurrency,
		TotalQueries:     res.TotalQueries,
		QPS:              res.QPS,
		P50:              res.P50,
		P95:              res.P95,
		P99:              res.P99,
		IOErrors:         res.IOErrors,
		IDMismatches:     res.IDMismatches,
		Counters:         res.Counters,
		UpstreamQueries:  res.UpstreamQueries,
		CoalescedQueries: res.CoalescedQueries,
		CacheEvictions:   res.CacheEvictions,
		Panics:           res.Panics,
		PprofCPU:         "cpu.pprof",
		PprofHeap:        "heap.pprof",
	})
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.md"), []byte(report), 0o644); err != nil {
		return res, err
	}

	configJSON, err := json.MarshalIndent(map[string]any{
		"scenario":    cfg.Scenario,
		"target":      cfg.Target,
		"admin":       cfg.Admin,
		"duration":    cfg.Duration.String(),
		"concurrency": cfg.Concurrency,
		"git_sha":     gitSha(),
		"go_version":  runtime.Version(),
	}, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "config.json"), configJSON, 0o644)
	}

	if res.IOErrors > 0 || res.IDMismatches > 0 {
		_ = os.WriteFile(filepath.Join(cfg.OutDir, "FAILED"),
			[]byte(fmt.Sprintf("io_errors=%d id_mismatches=%d", res.IOErrors, res.IDMismatches)),
			0o644)
		return res, fmt.Errorf("correctness failure: io_errors=%d id_mismatches=%d", res.IOErrors, res.IDMismatches)
	}

	return res, nil
}

// terminate sends SIGTERM to cmd's process, waits up to 10s for it to
// exit, then sends SIGKILL. Always called via defer.
func terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// capturePprof streams a pprof endpoint to disk. Used for both CPU and
// heap profiles.
func capturePprof(ctx context.Context, c *http.Client, url, outPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pprof %s status %d", url, resp.StatusCode)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func gitSha() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func hostString() string {
	out, err := exec.Command("uname", "-srm").Output()
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return strings.TrimSpace(string(out))
}
```

Imports needed at the top of `orchestrator.go` (extend the existing import block):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tantalor93/dnspyre/v3/pkg/reporter"
)
```

- [ ] **Step 2: Run `go build` to confirm the package compiles**

Run: `go build ./internal/stress/...`
Expected: builds without error.

- [ ] **Step 3: Run all existing unit tests**

Run: `go test ./internal/stress/...`
Expected: PASS for every test from Tasks 4–9. The new `Run` function has no unit tests yet — covered by Task 11.

- [ ] **Step 4: Commit**

```bash
git add internal/stress/orchestrator.go
git commit -m "Add stress orchestrator Run sequence

Run spawns mockupstream + bns (when --spawn is set), waits for /readyz,
scrapes /metrics before+after, captures CPU + heap pprof, calls
dnsbench.Benchmark.Run via the imported library, renders report.md,
and tears down subprocesses on the way out. Correctness failures
(IOError/IDmismatch) write a FAILED marker and return non-nil.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: cmd/bns-stress main + integration test

**Files:**
- Create: `cmd/bns-stress/main.go`
- Create: `cmd/bns-stress/main_test.go`

- [ ] **Step 1: Write `main.go`**

`cmd/bns-stress/main.go`:

```go
// Command bns-stress runs a BNS stress test. See
// docs/specs/2026-05-19-bns-dnspyre-stress-test-design.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bcrisp4/bns/internal/stress"
	_ "github.com/bcrisp4/bns/internal/stress/scenarios" // register scenarios
)

func main() {
	cfg := stress.Defaults()
	flag.StringVar(&cfg.Scenario, "scenario", cfg.Scenario, "Scenario name")
	flag.StringVar(&cfg.Target, "target", cfg.Target, "BNS DNS listener (host:port)")
	flag.StringVar(&cfg.Admin, "admin", cfg.Admin, "BNS admin endpoint (host:port)")
	flag.BoolVar(&cfg.Spawn, "spawn", cfg.Spawn, "Spawn a local bns + mockupstream (set --spawn=false to point at remote)")
	flag.DurationVar(&cfg.Duration, "duration", cfg.Duration, "Load generation duration")
	concurrency := flag.Uint("concurrency", uint(cfg.Concurrency), "dnspyre worker count")
	flag.IntVar(&cfg.RateLimit, "rate-limit", cfg.RateLimit, "Global queries/sec rate limit (0 = unbounded)")
	flag.StringVar(&cfg.BNSBin, "bns-bin", cfg.BNSBin, "Path to bns binary")
	flag.StringVar(&cfg.MockBin, "mock-bin", cfg.MockBin, "Path to mockupstream binary")
	flag.StringVar(&cfg.BlocklistPath, "blocklist", cfg.BlocklistPath, "Path to blocklist file (empty = bns default)")
	flag.StringVar(&cfg.OutDir, "out", cfg.OutDir, "Per-run output dir (empty = dist/stress/<ts>/)")
	flag.DurationVar(&cfg.PprofCPU, "pprof-cpu", cfg.PprofCPU, "CPU profile duration (0 = disabled)")
	flag.BoolVar(&cfg.PprofHeap, "pprof-heap", cfg.PprofHeap, "Capture heap profile at end of run")
	flag.Parse()
	cfg.Concurrency = uint32(*concurrency)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	res, err := stress.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bns-stress failed: %v (report=%s)\n", err, res.OutDir)
		os.Exit(1)
	}

	fmt.Printf("%s %s c=%d QPS=%.0f p99=%.1fms errors=%d blocked=%.1f%% report=%s\n",
		cfg.Scenario,
		cfg.Duration,
		cfg.Concurrency,
		res.QPS,
		float64(res.P99)/float64(1_000_000),
		res.IOErrors,
		blockedPct(res),
		res.OutDir,
	)
}

func blockedPct(r stress.Result) float64 {
	if r.TotalQueries == 0 {
		return 0
	}
	return 100.0 * float64(r.Counters["blocked"]) / float64(r.TotalQueries)
}
```

- [ ] **Step 2: Write the integration test (build-tagged)**

`cmd/bns-stress/main_test.go`:

```go
//go:build stress_integration

package main_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	_ "github.com/bcrisp4/bns/internal/stress/scenarios"
	"github.com/stretchr/testify/require"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestStress_MiniRun(t *testing.T) {
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	// cd up out of cmd/bns-stress.
	repoRoot = filepath.Join(repoRoot, "..", "..")

	binDir := filepath.Join(repoRoot, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	for _, target := range []struct{ out, pkg string }{
		{filepath.Join(binDir, "bns"), "./cmd/bns"},
		{filepath.Join(binDir, "mockupstream"), "./cmd/mockupstream"},
	} {
		build := exec.Command("go", "build", "-trimpath", "-o", target.out, target.pkg)
		build.Dir = repoRoot
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		require.NoErrorf(t, build.Run(), "build %s", target.pkg)
	}

	target := freePort(t)
	admin := freePort(t)
	outDir := t.TempDir()

	cfg := stress.Defaults()
	cfg.Scenario = "mixed"
	cfg.Target = target
	cfg.Admin = admin
	cfg.Duration = 2 * time.Second
	cfg.Concurrency = 4
	cfg.OutDir = outDir
	cfg.BNSBin = filepath.Join(binDir, "bns")
	cfg.MockBin = filepath.Join(binDir, "mockupstream")
	cfg.PprofCPU = 1 * time.Second
	cfg.PprofHeap = true

	// Use a small in-tree blocklist for the mini-run; the full hagezi pro.txt
	// is too heavy for an integration test.
	blocklist := filepath.Join(outDir, "blocklist.txt")
	require.NoError(t, os.WriteFile(blocklist, []byte("blocked.test\n"), 0o644))
	cfg.BlocklistPath = blocklist

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := stress.Run(ctx, cfg)
	require.NoError(t, err)
	require.Greater(t, res.TotalQueries, int64(0))
	require.FileExists(t, filepath.Join(outDir, "report.md"))
	require.FileExists(t, filepath.Join(outDir, "before.prom"))
	require.FileExists(t, filepath.Join(outDir, "after.prom"))
	require.FileExists(t, filepath.Join(outDir, "dnspyre-results.json"))
	require.FileExists(t, filepath.Join(outDir, "cpu.pprof"))
	require.FileExists(t, filepath.Join(outDir, "heap.pprof"))
	require.NoFileExists(t, filepath.Join(outDir, "FAILED"))
}
```

The test must use the `mixed` scenario's hard-coded `@scripts/stress/queries/mixed.txt`. That file must exist at the repo root at test time (Task 12 commits it). If running the integration test before Task 12, dnspyre will fail to load queries; that's expected ordering.

- [ ] **Step 3: Build everything**

Run: `make build-stress`
Expected: produces `bin/bns`, `bin/mockupstream`, `bin/bns-stress`.

- [ ] **Step 4: Run the integration test (requires Task 12 first if not yet done)**

If Task 12 (the query pool) is already in place:

Run: `go test -tags stress_integration ./cmd/bns-stress/...`
Expected: PASS, takes ~30s (2s run + build + setup).

If Task 12 is not yet done, skip this step and return after Task 12.

- [ ] **Step 5: Commit**

```bash
git add cmd/bns-stress/main.go cmd/bns-stress/main_test.go
git commit -m "Add bns-stress orchestrator binary + integration test

main wires the flag surface to stress.Config and prints the headline
summary line on success. The build-tagged integration test compiles the
trio, runs a 2-second mini-scenario, and asserts the per-run artefact
directory contents.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Query pool — committed mixed + blocked sample

**Files:**
- Create: `scripts/stress/build_mixed.sh`
- Create: `scripts/stress/queries/mixed.txt`
- Create: `scripts/stress/queries/blocked-sample.txt`

- [ ] **Step 1: Write the generator script**

`scripts/stress/build_mixed.sh`:

```bash
#!/usr/bin/env bash
# Regenerate scripts/stress/queries/mixed.txt deterministically.
# Composition:
#   50 cache-hot names    hot-0001.test … hot-0050.test
#   9000 cold names       cold-0001.test … cold-9000.test
#   950 blocked names     subset of hagezi pro.txt
set -euo pipefail

cd "$(dirname "$0")/queries"

HAGEZI="${HAGEZI:-/home/ben.guest/vendor/hagezi-dns-blocklists/domains/pro.txt}"
if [[ ! -f "$HAGEZI" ]]; then
    echo "hagezi pro.txt not found at $HAGEZI" >&2
    exit 1
fi

# Frozen subset: deterministic by sorting then taking every 500th line up
# to 950 entries. Re-running with a fresh hagezi snapshot updates the set.
grep -v '^#' "$HAGEZI" | sort | awk 'NR % 500 == 0' | head -n 950 > blocked-sample.txt
echo "wrote $(wc -l < blocked-sample.txt) blocked names to blocked-sample.txt"

{
    seq -f "hot-%04g.test" 1 50
    seq -f "cold-%04g.test" 1 9000
    cat blocked-sample.txt
} > mixed.txt
echo "wrote $(wc -l < mixed.txt) total names to mixed.txt"
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x scripts/stress/build_mixed.sh`

- [ ] **Step 3: Run it**

Run: `./scripts/stress/build_mixed.sh`
Expected output:

```
wrote 950 blocked names to blocked-sample.txt
wrote 10000 total names to mixed.txt
```

- [ ] **Step 4: Confirm contents**

Run: `wc -l scripts/stress/queries/*.txt`
Expected:

```
   950 scripts/stress/queries/blocked-sample.txt
 10000 scripts/stress/queries/mixed.txt
```

Run: `head -3 scripts/stress/queries/mixed.txt; echo --- ; tail -3 scripts/stress/queries/mixed.txt`
Expected: first lines are `hot-0001.test`, `hot-0002.test`, `hot-0003.test`. Last lines are entries from `blocked-sample.txt` (hagezi-derived).

- [ ] **Step 5: Run the integration test if not yet run**

Run: `go test -tags stress_integration ./cmd/bns-stress/... -v`
Expected: PASS, ~30s.

- [ ] **Step 6: Commit**

```bash
git add scripts/stress/build_mixed.sh scripts/stress/queries/mixed.txt scripts/stress/queries/blocked-sample.txt
git commit -m "Add committed query pool for the mixed scenario

50 cache-hot + 9000 cold + 950 hagezi-derived blocked names, generated
deterministically from build_mixed.sh. Committed so runs are reproducible
across hagezi updates; regenerate locally when refreshing the snapshot.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: Smoke run + final verification

**Files:** none modified.

- [ ] **Step 1: Build everything**

Run: `make build-stress`
Expected: produces `bin/bns`, `bin/mockupstream`, `bin/bns-stress`.

- [ ] **Step 2: Confirm dnspyre is not in `cmd/bns` import closure**

Run: `go list -deps ./cmd/bns | grep -i dnspyre || echo "OK: not imported"`
Expected: `OK: not imported`.

Run: `go list -deps ./cmd/bns-stress | grep -i dnspyre`
Expected: outputs the dnspyre packages (`github.com/tantalor93/dnspyre/v3/pkg/dnsbench`, `.../pkg/reporter`, etc).

- [ ] **Step 3: Run all unit tests**

Run: `make test`
Expected: PASS, no failures.

- [ ] **Step 4: Run race detector**

Run: `make race`
Expected: PASS.

- [ ] **Step 5: Run the integration test**

Run: `go test -tags stress_integration ./cmd/bns-stress/... -v`
Expected: PASS, ~30s.

- [ ] **Step 6: Run vet and lint**

Run: `make vet`
Expected: PASS.

(Skipped: `make lint` — `golangci-lint` not installed locally per CLAUDE.md; CI will run it.)

- [ ] **Step 7: Manual smoke run with the full mixed scenario**

Run (in foreground; press Ctrl-C if it runs too long):

```bash
./bin/bns-stress --scenario mixed --duration 10s --concurrency 10 \
  --blocklist examples/sample-blocklist.txt
```

Expected: at exit, prints a one-line summary, and `dist/stress/<ts>/report.md` is populated. Inspect `cat dist/stress/$(ls -1t dist/stress | head -1)/report.md` to confirm content.

- [ ] **Step 8: Commit any incidental fixes from the smoke run**

If the smoke run revealed bugs (e.g., a flag default wrong, a path off), fix them in a separate commit per fix:

```bash
git add <changed files>
git commit -m "<fix description>

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 9: Run `/simplify`**

Per the user's global CLAUDE.md: `ALWAYS run /simplify once all tasks in a plan are complete.`

Invoke the `simplify` skill against the diff for this work. Apply any improvements as their own commits.

---

## Self-review summary

Coverage by spec section:

| Spec § | Task                                  |
|--------|---------------------------------------|
| §1 Purpose             | Implicit — all tasks together         |
| §2 Non-goals           | Honoured throughout                   |
| §3 Topology            | Task 9 (helpers), Task 10 (Run)       |
| §4.1 mockupstream      | Tasks 2, 3                            |
| §4.2 cmd/bns-stress    | Task 11                               |
| §4.3 internal/stress   | Tasks 4, 5, 6, 7, 9, 10               |
| §4.4 query pool        | Task 12                               |
| §4.5 production --pprof| Task 8                                |
| §5 mixed scenario      | Task 5                                |
| §6 output artefacts    | Tasks 7, 10                           |
| §7 testing strategy    | TDD throughout; Task 11 integration   |
| §8 Makefile            | Task 1 step 5                         |
| §9 dependencies        | Task 1 step 3                         |
| §10 decision log       | Captured in spec only                 |
| §11 follow-ups         | Out of scope (YAGNI)                  |

Type-consistency check: the `dnsbench.Benchmark` field names used in Task 5 (`Server`, `Types`, `Concurrency`, `Duration`, `Probability`, `Queries`, `Recurse`, `Rcodes`, `HistDisplay`, `HistPre`, `HistMin`, `HistMax`, `Writer`, `Silent`, `ProgressBar`, `JSON`) all match the struct definition observed in dnspyre v3.11.0 (`pkg/dnsbench/benchmark.go` lines 71–224). `reporter.Merge(b, results) BenchmarkResultStats` and the `Counters` field names (`Total`, `IOError`, `IDmismatch`) match `pkg/reporter/merge.go`. `admin.WithPprof`, `admin.New(ln, reg, rdy, opts...)` are introduced in Task 8 and consumed only by `cmd/bns/serve.go` in the same task.
