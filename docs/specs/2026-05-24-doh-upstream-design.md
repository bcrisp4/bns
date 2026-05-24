# BNS DoH Upstream — Design Spec

- **Date:** 2026-05-24
- **Status:** Draft (pending implementation plan)
- **Author:** Ben Crisp
- **Builds on:** `docs/specs/2026-05-19-bns-mvp-design.md`, `docs/specs/2026-05-21-bns-http-blocklist-source-design.md`

## 1. Context

BNS today forwards DNS queries over plain UDP (with automatic TCP retry on
truncation) via the `internal/upstream.UDPClient`. Plain UDP traffic is
unencrypted, observable to any on-path party (ISP, network operator, public
Wi-Fi), and increasingly out of step with modern recursive-resolver
expectations for self-hosted DNS forwarders.

This spec adds DNS-over-HTTPS (DoH, RFC 8484) as an alternative upstream
transport alongside UDP. Multiple upstreams of either type can coexist in
the `upstreams:` pool with the existing declared-order failover semantics
preserved. The change is additive at the `Upstream` interface boundary;
the resolver chain, cache stage, coalesce stage, metrics collectors, and
blocklist subsystem are largely untouched.

Scope is intentionally narrow: DoH only. DoT (DNS-over-TLS on port 853)
and DoH3 (over HTTP/3 / QUIC) are not included and are tracked as future
work.

## 2. Goals

1. Operators can declare DoH upstreams alongside UDP upstreams in YAML
   configuration.
2. DoH queries use HTTP/2 (with HTTP/1.1 fallback) over TLS 1.3 (minimum),
   reusing a long-lived connection where the server permits.
3. Endpoint hostname resolution is **never** delegated to the system
   resolver or to BNS's own resolver chain. Operators supply the IPs
   directly (`endpoint_ips`), eliminating the bootstrap deadlock that
   would otherwise occur on Pi-hole-style deployments where BNS itself is
   the system resolver.
4. DoH client behaviour is compliant with RFC 8484 in the areas that
   matter for a forwarder: HTTP method, headers, status handling,
   Content-Type parsing, `Age` header handling, body cap, redirect
   refusal, server-push refusal, cookie absence.
5. DoH integrates transparently with the existing resolver chain — cache,
   singleflight coalescing, metrics, and query logging all work without
   transport-specific code.
6. Per-upstream Prometheus metrics distinguish UDP from DoH via a new
   `protocol` label and surface DoH-specific failure modes (HTTP status,
   TLS handshake outcome).
7. Per-query attribution lands in the query log: forwarded queries
   include `upstream` and `upstream_protocol` attrs identifying which
   forwarder served each query (absent for cache hits, blocks, and
   coalesce piggybacks).
8. Pre-existing UDP-only configurations continue working unchanged after
   the upgrade (`type:` defaults to `udp`).

## 3. Non-goals

The following are explicitly out of scope and either deferred or rejected:

- **DoT** (DNS-over-TLS) and **DoH3** (over HTTP/3). Will be separate
  specs if and when demand surfaces.
- **DoH server** (BNS receiving DoH from clients). BNS remains a
  UDP/TCP-listening forwarder.
- **EDNS(0) padding** (RFC 8467). RFC 8484 does not require client
  padding; defer until measured to matter.
- **DNS Stamps** (`sdns://...`) as a config format. Opaque to operators;
  rejected on readability grounds.
- **DDR / Designated Resolver auto-discovery** (RFC 9462). Niche standard;
  defer until client/server support matures.
- **Encrypted Client Hello** (RFC 9180). Not standardised enough to ship.
- **TLS InsecureSkipVerify** as a config knob. Never. Documented refusal.
- **`tls_servername` override** field. Real-world DoH providers'
  certificates always include hostname SANs; defer the override until
  concrete demand is observed.
- **Circuit breaking / health-aware Pool routing.** Existing sequential
  declared-order failover is preserved. Smarter routing belongs in a
  dedicated spec.
- **Multi-DoH-endpoint racing / fastest-first.** Same.
- **Sidecar pattern** (`cloudflared proxy-dns` / `dnscrypt-proxy` on
  localhost). Rejected because it fragments the "single static binary"
  deploy posture that the project is committed to. Documented as an
  alternative in §11.

## 4. Architecture

### 4.1 Upstream interface

The existing `internal/upstream.Upstream` interface is extended with two
identity getters so that the Pool no longer needs parallel slices of
names and protocols alongside the upstream clients themselves:

```go
type Upstream interface {
    Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error)
    Name() string      // metric label, e.g. "1.1.1.1:53" or the DoH URL
    Protocol() string  // "udp" | "doh"
}
```

`UDPClient` gains two trivial methods (`Name() = c.addr`,
`Protocol() = "udp"`). The new `DoHClient` implements the same. Future
transports (DoT, DoH3) will slot in identically.

### 4.2 Package layout

```
internal/upstream/
  upstream.go        (Upstream interface — extended)
  pool.go            (single []Upstream slice; metric labels via interface)
  udp_client.go      (unchanged behaviour, plus Name/Protocol)
  doh_client.go      NEW
  factory.go         NEW — New(config.Upstream) → Upstream
  testutil/
    spawn.go         (existing in-process DNS server)
    tlscert.go       NEW — generate self-signed cert with custom SANs
```

### 4.3 Resolver chain integration

The resolver chain is unchanged:

```
metrics → qlog → block → cache → coalesce → forward → Pool → {UDPClient | DoHClient}
```

The `forward` stage calls `pool.Exchange(ctx, req)` and is transport-agnostic.
`coalesce` (singleflight) sits above `forward` and benefits even more from
DoH than UDP because every prevented upstream call saves an HTTP/2 stream
and decrypt cost rather than just a UDP packet round-trip.

### 4.4 Per-query upstream attribution (ctx marker)

A new ctx-marker pattern, mirroring the existing `WithBlockMarker` /
`MarkBlocked` / `Outcome` triad, propagates upstream identity back up
the chain so the query log can record which forwarder served each query.

- `metricstage` installs the marker (`WithUpstreamMarker`) at the top of
  every query alongside the existing block marker.
- `Pool.Exchange` calls `MarkUpstream(ctx, name, protocol)` immediately
  after a successful per-upstream `Exchange` returns, capturing the
  winner of the sequential-failover loop.
- `qlog` reads `UpstreamInfoFrom(ctx)` and emits `upstream` +
  `upstream_protocol` attrs when present.
- Cache hits, blocked queries, and coalesce piggybacks never reach Pool
  in their own ctx, so the marker stays unset; qlog omits the attrs
  cleanly (same conditional pattern as the existing optional `client` /
  `proto` attrs).

This is the resolver-chain side of the "per-query attribution" gap
identified during design review — aggregate `bns_upstream_*` metrics
alone require log↔metric timestamp correlation to answer "which
forwarder served *this* specific query", which is operationally awkward.
The ctx-marker addition is ~30 LOC across `outcome.go`, `metricstage`,
`pool.go`, and `qlog.go`.

### 4.4 Pool behaviour (unchanged)

Sequential declared-order failover. First upstream in the YAML is the
primary; subsequent entries are fallbacks consulted only when the primary
returns an error. SERVFAIL / NXDOMAIN / REFUSED from a successful exchange
are valid DNS responses and do not trigger failover. No parallel racing,
no load balancing, no circuit breaking.

This means the operator's `upstreams:` ordering choice is operationally
load-bearing:

| Layout | Behaviour |
|---|---|
| `[DoH, UDP]` | Privacy-first. Every query encrypted in steady state; cleartext fallback if DoH dies. |
| `[UDP, DoH]` | Speed-first. UDP normally; DoH almost never exercised → cold-start hits frequent. |
| `[DoH, DoH]` | Pure encrypted. Multiple providers for resilience. |
| `[UDP]` | Today's behaviour preserved. |

The container default switches to `[DoH-primary, UDP-fallback]` (§9).

## 5. Configuration schema

### 5.1 `config.Upstream` struct

```go
type Upstream struct {
    Type        string        `mapstructure:"type"`         // "udp" | "doh"; default "udp"
    Addr        string        `mapstructure:"addr"`         // type=udp: required
    URL         string        `mapstructure:"url"`          // type=doh: required, must be https://
    EndpointIPs []string      `mapstructure:"endpoint_ips"` // type=doh: required, non-empty
    Timeout     time.Duration `mapstructure:"timeout"`      // required > 0
}
```

### 5.2 YAML examples

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

### 5.3 Validation rules

`config/validate.go` enforces, in addition to the existing pool-non-empty
and per-upstream-timeout-positive checks:

- `type` ∈ `{"", "udp", "doh"}`. Empty defaults to `"udp"`.
- `type=udp`: `addr` parses via `net.SplitHostPort`; port in `[0, 65535]`.
  `url`, `endpoint_ips` rejected if set (typo defense).
- `type=doh`:
  - `url` parses via `net/url.Parse`; scheme MUST be `https` (RFC 8484
    §5).
  - URL host MUST be a hostname (i.e. `net.ParseIP(host) == nil`). IP-
    literal URLs are rejected.
  - `endpoint_ips` MUST be non-empty; each entry parses via
    `net.ParseIP`.
  - `addr` rejected if set.
- Cross-field: if `blocklists.sources` contains any `type=http` entry,
  the union of upstream-derived bootstrap addresses (see §7.2) MUST be
  non-empty. Effectively automatic today since DoH always carries
  `endpoint_ips`, but defends against future schema loosening.

### 5.4 Defaults

`config/defaults.go` is unchanged. `Type` empty-string is handled by the
validate switch (treated as `udp`) and by the factory dispatch. No
default values for `URL`, `EndpointIPs`. `Timeout` retains its existing
zero-is-error semantics.

### 5.5 CLI flag removal

The `--upstream` cobra flag is removed entirely. Upstreams are YAML-only
(consistent with `blocklists.sources`, which has been YAML-only since
HTTP source landed because viper's `AutomaticEnv` cannot index slice
config). This is a breaking change for CLI users; release notes call it
out (§10).

## 6. DoH client implementation

### 6.1 Library posture

DoH client is hand-rolled over stdlib `net/http`, using only the
`dnshttp.Response` helper from `codeberg.org/miekg/dns/dnshttp` for
response decoding. The `dnshttp.NewRequest` helper is **not** used
because it unconditionally appends `/dns-query` to the supplied URL
(its docstring explicitly says "URL should not have a path"). Using
it with the operator-natural `https://host/dns-query` config would
produce a double-appended path and a 404. Building the request directly
is five lines and removes the footgun. Encoding is one `req.Pack()` call
followed by `http.NewRequestWithContext` and three header sets.

Rejected alternative: AdGuard's `dnsproxy` library. Ten new transitive
deps including `quic-go` for a DoH-only feature. Overkill.

### 6.2 Constructor

```go
type DoHClient struct {
    url        string
    httpClient *http.Client
    logger     *slog.Logger
    metrics    *metrics.Metrics
}

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
    host := u.Hostname()  // = SNI / cert SAN target
    port := u.Port()
    if port == "" {
        port = "443"
    }

    var rr atomic.Uint32
    netDialer := &net.Dialer{Timeout: timeout}

    transport := &http.Transport{
        TLSClientConfig: &tls.Config{
            ServerName: host,                              // SNI = URL hostname
            NextProtos: []string{"h2", "http/1.1"},        // h2 preferred, h1 fallback
            MinVersion: tls.VersionTLS13,                  // BCP 195 / RFC 9325
        },
        // Intercept TCP dial; net/http wraps with TLS using TLSClientConfig.
        DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
            start := int(rr.Add(1)-1) % len(endpointIPs)
            var lastErr error
            for i := 0; i < len(endpointIPs); i++ {
                ip := endpointIPs[(start+i)%len(endpointIPs)]
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
        return nil, fmt.Errorf("doh %s: configure h2: %w", rawURL, err)
    }
    // Disable HTTP/2 server push (RFC 8484 §5.3: client MUST establish
    // pushed URI is usable before using the data; easiest compliance is
    // to refuse push entirely).
    // (http2.Transport on configured transport — set push handler to nil
    // or configure via the underlying http2.Transport; details in plan.)

    return &DoHClient{
        url:    rawURL,
        logger: logger,
        metrics: mtr,
        httpClient: &http.Client{
            Transport: transport,
            Timeout:   timeout,
            // RFC 8484 §9: a hijacked DoH server should not be able to
            // redirect to a logging endpoint. Refuse all redirects.
            CheckRedirect: func(*http.Request, []*http.Request) error {
                return http.ErrUseLastResponse
            },
            // RFC 8484 §8: cookies SHOULD NOT be accepted by DoH clients.
            // Default http.Client.Jar is nil; explicit for clarity.
            Jar: nil,
        },
    }, nil
}
```

Why no cipher pinning, why TLS 1.3 floor: confirmed via research that Go
stdlib auto-detects ARMv8 Crypto Extensions (AES + PMULL) on Cortex-A76
and reorders TLS 1.3 cipher suites to prefer hardware-accelerated AES-GCM
on the Pi 5. Pinning specific suites adds risk (lockout, future-Go
breakage) for zero throughput gain. TLS 1.3 floor is the only TLS knob
worth touching: 1-RTT handshake, modern forward-secrecy guarantees,
universally supported by every public DoH provider.

### 6.3 Exchange method

```go
func (c *DoHClient) Exchange(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
    // RFC 8484 §4.1.1: DoH clients using a media format that includes the
    // ID field SHOULD use a DNS ID of 0 in every request. Rationale: HTTP
    // caches key on the body; varying IDs would fragment caches. Save the
    // caller's ID, zero it for Pack, restore via defer so Upstream's
    // "MUST NOT mutate req" contract holds across every return path.
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
    httpReq.Header.Set("Content-Type", "application/dns-message")
    httpReq.Header.Set("Accept", "application/dns-message")
    httpReq.Header.Set("User-Agent", "bns/"+version.Version)

    // Attach httptrace for cold-start logging + TLS handshake counter.
    trace := &httptrace.ClientTrace{
        GotConn: func(info httptrace.GotConnInfo) {
            if !info.Reused {
                c.logger.Debug("doh new connection",
                    "upstream", c.url, "addr", info.Conn.RemoteAddr())
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

    statusLabel := strconv.Itoa(resp.StatusCode)
    c.metrics.DoHHTTPStatusTotal.WithLabelValues(c.url, statusLabel).Inc()

    if resp.StatusCode/100 != 2 {
        return nil, fmt.Errorf("doh %s: http %d", c.url, resp.StatusCode)
    }

    // RFC 8484 §4.2 + RFC 7231 §3.1.1.1: Content-Type compare is case-
    // insensitive on the type token, and parameters (e.g. charset=utf-8)
    // are permitted. Exact string compare would over-reject.
    mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
    if err != nil || !strings.EqualFold(mediaType, "application/dns-message") {
        return nil, fmt.Errorf("doh %s: unexpected content-type %q",
            c.url, resp.Header.Get("Content-Type"))
    }

    // RFC 8484 §6 + defense-in-depth: cap response body at DNS max msg
    // size. A malicious or buggy server could otherwise inflate the
    // allocator.
    resp.Body = io.NopCloser(io.LimitReader(resp.Body, dns.MaxMsgSize))

    msg, err := dnshttp.Response(resp)
    if err != nil {
        return nil, fmt.Errorf("doh %s: decode: %w", c.url, err)
    }

    // RFC 8484 §5.1: when the HTTP Age header indicates time the response
    // has been held by an HTTP intermediate cache (CDN, proxy), the
    // client SHOULD subtract that age from the DNS RR TTLs. The wire
    // payload's TTLs reflect remaining-TTL when the response arrived at
    // the intermediate cache, NOT when it reached us. Without this,
    // downstream caching (here and at the client) overstates remaining
    // freshness and serves stale answers for an additional Age seconds.
    //
    // Worked example: authoritative TTL = 300s.
    //   T=0    Auth emits msg{TTL=300}.
    //   T=60   Origin DoH server returns msg{TTL=240} (decremented its
    //          own 60s hold).
    //   T=90   Reaches HTTP intermediate cache. Cached as-is.
    //   T=120  We query → CDN returns body{TTL=240} + Age:30.
    //          Without Age handling: BNS caches TTL=240, expiring at
    //          T=360 — 90s past true expiry at T=300.
    //          With Age handling: BNS caches TTL=210, expiring at T=330.
    //
    // Most direct connections to public DoH providers will have no Age
    // header at all (no intermediate cache between BNS and the resolver).
    // Defensive correctness costs ~6 lines and zero per-query work when
    // the header is absent.
    if ageStr := resp.Header.Get("Age"); ageStr != "" {
        if age, parseErr := strconv.ParseUint(ageStr, 10, 32); parseErr == nil && age > 0 {
            decrementTTLs(msg, uint32(age))
        }
    }

    // Restore caller's ID on the response so downstream stages and the
    // wire writer match the originating query.
    msg.ID = origID
    return msg, nil
}

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

### 6.4 RFC 8484 compliance summary

Audit performed against the full RFC text. The compliance posture of the
above implementation:

| RFC section | Requirement | Status |
|---|---|---|
| §4.1 method | POST acceptable; GET not required for clients. | OK |
| §4.1 Content-Type | `application/dns-message` on POST body. | OK |
| §4.1 Accept | `application/dns-message`. | OK |
| §4.1.1 ID | DNS ID = 0 SHOULD on the wire. | OK (saved/restored). |
| §4.2 status | 2xx carries payload (not strictly `== 200`). | OK (`/100 == 2`). |
| §4.2 response Content-Type | Case-insensitive type compare; allow parameters. | OK (`mime.ParseMediaType`). |
| §5 scheme | MUST use `https` URI scheme. | OK (validate.go rejects others). |
| §5.2 HTTP/2 | HTTP/2 is the minimum RECOMMENDED. | OK (`ForceAttemptHTTP2 + NextProtos`); HTTP/1.1 fallback preserves operability against degraded servers. |
| §5.3 server push | Client MUST establish pushed URI is usable for DoH before using push data. | OK (push disabled on transport). |
| §5.1 Age header | Client SHOULD subtract Age from RR TTLs. | OK (`decrementTTLs`). |
| §6 media type | MUST support `application/dns-message`. | OK. |
| §6 body size | 65535-byte DNS msg cap. | OK (`io.LimitReader(dns.MaxMsgSize)`). |
| §8 cookies | Client SHOULD NOT accept cookies. | OK (`Jar: nil`). |
| §9 redirects | Client MUST NOT use a different URI than configured. | OK (`CheckRedirect: ErrUseLastResponse`). |
| §9 ALPN / alt-svc | Don't honour out-of-band URI switching. | OK (no `Alt-Svc` handling). |
| BCP 195 (RFC 9325) TLS hygiene | TLS 1.3 minimum. | OK (`MinVersion: VersionTLS13`). |

### 6.5 Factory dispatch

```go
// internal/upstream/factory.go
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

## 7. Wiring changes

### 7.1 `serve.go` upstream loop

Before:

```go
ups := make([]upstream.Upstream, 0, len(cfg.Upstreams))
names := make([]string, 0, len(cfg.Upstreams))
for _, u := range cfg.Upstreams {
    ups = append(ups, upstream.NewUDPClient(u.Addr, u.Timeout))
    names = append(names, u.Addr)
}
pool := upstream.NewPool(ups, names, mtr)
```

After:

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

`upstreamAddrs` helper is removed and replaced with `upstreamDialAddrs`
(§7.2). The startup `warmupProbe` continues to use the Pool unchanged
and naturally pre-warms DoH connections when DoH is the primary upstream.

### 7.2 Blocklist bootstrap aggregation

`BootstrapResolver` mechanism is unchanged: stdlib pure-Go resolver with
custom `Dial` that round-robins across operator-supplied DNS server
addresses. The IP source list expands to include DoH endpoint IPs paired
with `:53`:

```go
func upstreamDialAddrs(ups []config.Upstream) []string {
    var out []string
    for _, u := range ups {
        switch u.Type {
        case "", "udp":
            out = append(out, u.Addr)
        case "doh":
            for _, ip := range u.EndpointIPs {
                out = append(out, net.JoinHostPort(ip, "53"))
            }
        }
    }
    return out
}
```

`serve.go:143`:

```go
bootstrapResolver := blocklist.NewBootstrapResolver(upstreamDialAddrs(cfg.Upstreams))
```

This embeds an assumption: the IPs serving DoH on `:443` also serve plain
UDP/TCP DNS on `:53`. True for every major public DoH provider
(Cloudflare 1.1.1.1, Google 8.8.8.8, Quad9 9.9.9.9, AdGuard, NextDNS,
Mullvad). Documented as a known limitation for self-hosted DoH-only
endpoints; follow-up tracked in `docs/TODO.md` under "DoH upstream
(2026-05-24-doh-upstream)". Likely future direction is a top-level
`bootstrap_addrs:` list decoupled from upstream config, NOT per-source
blocklist IP pinning (blocklist URLs typically sit behind CDNs whose IP
ranges drift, so per-source pinning would rot silently).

### 7.3 Pool change

The `Pool` struct drops its parallel `names []string` slice; metric
labels now come from `u.Name()` / `u.Protocol()` on the interface.
Constructor signature simplifies to `NewPool(ups []Upstream, mtr
*metrics.Metrics)`.

On the first per-upstream `Exchange` that returns without error, Pool
also calls `resolver.MarkUpstream(ctx, u.Name(), u.Protocol())` so the
query-log stage can surface the winning forwarder. Marking is a no-op
if no marker was installed on this ctx (preserving the existing pattern
for fakeUpstream tests that bypass `metricstage`).

### 7.3.1 New `resolver` helpers

`internal/resolver/outcome.go` gains a parallel set of helpers to the
existing block-marker triad:

```go
type UpstreamInfo struct {
    Name     string // metric label, e.g. URL or "host:port"
    Protocol string // "udp" | "doh"
}

type upstreamInfoKey struct{}

func WithUpstreamMarker(ctx context.Context) (context.Context, *UpstreamInfo) {
    var info UpstreamInfo
    return context.WithValue(ctx, upstreamInfoKey{}, &info), &info
}

func MarkUpstream(ctx context.Context, name, protocol string) {
    if info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo); ok && info != nil {
        info.Name = name
        info.Protocol = protocol
    }
}

func UpstreamInfoFrom(ctx context.Context) (UpstreamInfo, bool) {
    info, ok := ctx.Value(upstreamInfoKey{}).(*UpstreamInfo)
    if !ok || info == nil || info.Name == "" {
        return UpstreamInfo{}, false
    }
    return *info, true
}
```

`internal/resolver/metricstage/metrics.go` adds one line beside the
existing `WithBlockMarker` call:

```go
ctx, _ = resolver.WithBlockMarker(ctx)
ctx, _ = resolver.WithUpstreamMarker(ctx)  // NEW
```

`internal/resolver/qlog/qlog.go` adds an optional attr block after the
existing client/proto block:

```go
if info, ok := resolver.UpstreamInfoFrom(ctx); ok {
    attrs = append(attrs,
        slog.String("upstream", info.Name),
        slog.String("upstream_protocol", info.Protocol),
    )
}
```

Cache hits and blocked queries leave the marker unset (forward stage
never runs); coalesce piggybackers operate on their own ctx, separate
from the winning singleflight call, so they also leave the marker unset.
Behaviour documented: per-query attribution is recorded only for queries
that actually exchanged with an upstream.

### 7.4 Metrics

`internal/metrics/metrics.go` gains a `protocol` label on the two
existing upstream vectors and adds two new DoH-specific vectors:

```go
UpstreamQueriesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_upstream_queries_total",
    Help: "Total upstream queries, by upstream, protocol and outcome.",
}, []string{"upstream", "protocol", "outcome"})

UpstreamDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
    Name: "bns_upstream_duration_seconds",
    Help: "Upstream exchange duration in seconds, by upstream and protocol.",
    Buckets: prometheus.DefBuckets,
}, []string{"upstream", "protocol"})

DoHHTTPStatusTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_doh_http_status_total",
    Help: "DoH HTTP responses, by upstream and HTTP status code (or 'timeout' / 'transport_error' for pre-response failures).",
}, []string{"upstream", "status"})

DoHTLSHandshakesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
    Name: "bns_doh_tls_handshakes_total",
    Help: "DoH TLS handshakes, by upstream and result. High rate indicates connection churn.",
}, []string{"upstream", "result"})
```

The `protocol` label is a low-cardinality addition (2 values × existing
upstreams) that lets operators slice "DoH error rate vs UDP error rate"
without name-substring matching. The two new vectors are diagnostic gold
for "why is my DoH upstream slow today" — TLS handshake rate exposes
connection churn driven by `IdleConnTimeout`, and HTTP status separates
DNS-layer failures from HTTP-layer failures.

The `protocol` label change is breaking for any pre-existing Prometheus
alerts that label-match exactly on `{upstream}` without protocol;
release notes call it out (§10).

## 8. Testing

### 8.1 Unit — `internal/upstream/doh_client_test.go`

Uses `httptest.NewUnstartedServer` with a custom TLS config built via a
new `internal/upstream/testutil/tlscert.go` helper that generates
self-signed certs with caller-specified SANs (DNS names + IP addresses).
The DoH handler decodes via `dnshttp.Request` and encodes via the
mirror pattern that `internal/upstream/testutil.Spawn` already uses for
UDP/TCP.

Test cases:

- Round-trip success against a stub TLS server.
- `req.ID == 0` on the wire; caller's `req.ID` unchanged; response
  `msg.ID` restored to original.
- POST method, both `Content-Type` and `Accept` headers correct,
  `User-Agent` includes BNS version.
- Non-2xx HTTP status returns wrapped error mentioning the status.
- 3xx redirect: refused (`CheckRedirect: ErrUseLastResponse`); redirect
  target server's hit counter stays at zero across N calls.
- `Content-Type: application/dns-message; charset=utf-8` accepted (case-
  insensitive type compare).
- `Content-Type: text/plain` rejected.
- Response body >`dns.MaxMsgSize` truncated to cap; decode error
  surfaces cleanly with no OOM.
- `Age: 60` decrements TTLs by 60s; SOA `Minttl` decremented too on
  negative-response case.
- `Age: 120` against TTL=30 floors at zero.
- Absent `Age` header: TTLs unchanged.
- Endpoint round-robin: two stub servers, single client config with
  `endpoint_ips=[ip1, ip2]` (both IP-literal URLs to dodge the SAN/port
  substitution complexity covered in §8.4); across N requests, both
  servers receive traffic.
- Failover on dial error: first endpoint IP unreachable, second succeeds.
- TLS 1.3 floor: server allows only TLS 1.2 → handshake fails.
- `httpClient.Jar == nil`.
- `Pack` error path: caller's `req.ID` restored, error returned.
- Context cancellation: returns wrapped `context.Canceled`.
- `Name()` returns URL, `Protocol()` returns `"doh"`.

### 8.2 Unit — `internal/config/validate_test.go` additions

- Missing `url` on `type=doh`.
- Non-`https` scheme.
- IP-literal URL (rejected — we mandate hostname URL form).
- Hostname URL without `endpoint_ips`.
- Invalid IP string in `endpoint_ips`.
- `addr` set on `type=doh`.
- `url` set on `type=udp`.
- Unknown `type` value.
- Empty `type` defaults to UDP with valid `addr` (passes).
- IPv6 hostname URL (e.g. `https://dns.example/dns-query` is fine; an
  IPv6 host literal — which we reject as IP literal anyway — needs no
  special test).
- HTTP blocklist source present with zero upstream-derived bootstrap
  IPs (no UDP, no DoH) → validation error.

### 8.3 Unit — `internal/upstream/pool_test.go` updates

`fakeUpstream` gains `Name()` + `Protocol()` methods. Add tests:

- Mixed UDP + DoH fakes — both Exchange paths exercised, metric labels
  asserted via `prometheus.testutil` helpers.
- Metric labels include the new `protocol` label correctly.
- Pool calls `MarkUpstream` on success: ctx pre-installed with
  `WithUpstreamMarker`; after `Pool.Exchange` succeeds, `UpstreamInfoFrom`
  returns the winning upstream's name + protocol.
- Pool does NOT mark on all-fail path: ctx-marker stays unset.
- Pool fallover path marks the **secondary** upstream when primary errors
  and secondary succeeds.

### 8.3.1 Unit — `internal/resolver/outcome_test.go` additions

Round-trip the new marker:

- `UpstreamInfoFrom` returns `(_, false)` on ctx with no marker.
- After `WithUpstreamMarker` + `MarkUpstream(ctx, "x", "doh")`,
  `UpstreamInfoFrom` returns the values.
- `MarkUpstream` on a ctx without a marker is a safe no-op.

### 8.3.2 Unit — `internal/resolver/qlog/qlog_test.go` additions

- ctx carries `UpstreamInfo{Name: "https://...", Protocol: "doh"}` →
  emitted log line includes both `upstream` and `upstream_protocol`
  attrs.
- ctx carries no marker → attrs absent (existing assertions still pass).
- Block synthesis path: marker installed but never marked (forward never
  ran) → attrs absent.

### 8.4 Integration — `internal/integration/integration_doh_test.go`

- `httptest.NewUnstartedServer` with the new testutil `tlscert.go`
  helper supplying SAN for `127.0.0.1`.
- Boot BNS in-process with a config containing one DoH upstream pointing
  at the stub (IP-literal URL form for tests).
- Send a DNS query via miekg client UDP → BNS UDP listener → DoH
  upstream → response.
- Assert correct answer; assert
  `bns_upstream_queries_total{protocol="doh",outcome="ok"} == 1`.

### 8.5 Hostname-URL automated coverage — pragmatic scope cap

The hostname-URL form (e.g. `url: https://test.invalid/dns-query` with
`endpoint_ips: [127.0.0.1]`) is exercised by IP-literal-URL tests for
every code path EXCEPT the SNI-from-URL-hostname behaviour. Wiring an
isolated test for that path requires:

- A test seam for substituting a non-`:443` bootstrap port (httptest
  uses ephemeral ports).
- A test seam for injecting custom `RootCAs` (production TLS uses system
  roots).
- A testutil-generated cert with the `test.invalid` SAN.

Building those seams is more production-code complexity than the
remaining uncovered branch warrants. Coverage stance: ship without the
test seams. The uncovered behaviour is a one-line `tls.Config{ServerName:
host}` that derives from the URL parser — a future refactor that breaks
it would also break the IP-literal SNI-override path. Manual smoke test
recipe against a real DoH endpoint is documented in §9.

### 8.6 Race detector

Every new test passes under `make race`. Round-robin index counter is
atomic; shared `http.Client` is stdlib-safe across goroutines.

## 9. Container, docs, operator-facing changes

### 9.1 `examples/config.example.yaml`

Replace the UDP-only block with a mixed UDP + DoH example so both forms
are demonstrated:

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

### 9.2 `deploy/docker/config.yaml`

Switch the baked container default to DoH-primary with UDP fallback.
The container is the "drop-in, run" path; defaulting to encrypted
upstream is the correct modern posture, with the cleartext fallback
preserving availability when DoH is unreachable:

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

### 9.3 `CLAUDE.md`

- Update Quickstart: drop `--upstream` flag, point at `-c
  examples/config.example.yaml`.
- Update Architecture > Package layout: add `doh_client.go`,
  `factory.go` under `internal/upstream/`.
- Update Architecture > Key interfaces table:
  - `Upstream` row: add `Name()` and `Protocol()` to the contract;
    "Future impls" gains "DoT, DoH3" (DoH ticked off).
- Update Tech stack — non-obvious: add a line about the DoH client
  living in `internal/upstream/doh_client.go`, hand-rolled over stdlib
  `net/http`, using `dnshttp.Response` from miekg for decode but
  bypassing `dnshttp.NewRequest` to avoid its `/dns-query` double-
  append behaviour.
- Update Gotchas: add three entries:
  - "DoH upstreams require `endpoint_ips` — operator-pinned IPs.
    BNS never resolves the DoH URL hostname (would deadlock through
    itself)."
  - "Removed `--upstream` CLI flag. Upstreams YAML-only (matches
    blocklists, which have been YAML-only since HTTP source landed)."
  - "DoH endpoint_ips also pressed into service as plain DNS bootstrap
    targets for the blocklist fetcher (paired with `:53`). True for
    every major public provider; documented limitation for self-hosted
    DoH-only endpoints."

### 9.4 Manual smoke recipe

A spec-internal recipe (also worth including in CLAUDE.md as part of
the build/test section) for confirming DoH:

```bash
make build
./bin/bns serve -c examples/config-doh.yaml \
    --listen.udp 127.0.0.1:5354 \
    --listen.tcp 127.0.0.1:5354 &
dig @127.0.0.1 -p 5354 example.com   # → forwarded via DoH
curl -s http://127.0.0.1:9090/metrics | grep -E 'bns_upstream_queries_total\{.*protocol="doh"'
curl -s http://127.0.0.1:9090/metrics | grep bns_doh_tls_handshakes_total
```

## 10. Rollout, migration, release

### 10.1 Migration

Pre-existing UDP-only YAML configs continue to work unchanged. Empty
`type` is coerced to `"udp"` by validation. Container deployments
upgrading from v0.3.x pick up the new DoH-primary default
automatically (assuming they use the baked config).

Pre-existing CLI usage of `--upstream` is broken by this release. Users
relying on the flag must move to YAML config. The Quickstart in
`CLAUDE.md` is updated to show the YAML path.

### 10.2 Release

Tag: `v0.4.0`. Container: `ghcr.io/bcrisp4/bns:0.4.0` + `:0.4` + `:latest`
via existing buildx multi-arch workflow. No infra or schema-migration
changes required.

Release notes prominently call out:

1. New DoH upstream support; example config shown.
2. `--upstream` CLI flag removed.
3. `bns_upstream_*` metric label change: new `protocol` label. Pre-
   existing alerts that label-match `{upstream}` without protocol
   continue to work; alerts that omit `{upstream=...}` and group by all
   labels need updates.
4. Container default switched to DoH-primary. Operators wanting the old
   plain-UDP-only behaviour mount their own config.
5. Query log gains two optional fields per forwarded query:
   `upstream` (forwarder name / URL) and `upstream_protocol` (`udp` /
   `doh`). Absent for cache hits, blocked queries, and coalesce
   piggybacks. Log consumers that strict-schema-match on attribute set
   may need updates.

### 10.3 Test plan

1. `make race` clean.
2. `make test` clean.
3. Manual smoke: build, run with mixed UDP + DoH config, `dig` against
   `127.0.0.1:5354`, verify response, confirm DoH metrics non-zero.
4. Manual failover smoke: run with primary upstream pointing at an
   unreachable IP; verify failover to secondary recorded in metrics
   with `outcome="error"` on the primary and `outcome="ok"` on the
   secondary.
5. Manual smoke against a real public DoH provider (Cloudflare): drop a
   YAML with `url: https://cloudflare-dns.com/dns-query` +
   `endpoint_ips: [1.1.1.1, 1.0.0.1]`; verify hostname-URL form works
   end-to-end including SNI / cert validation.
6. Stress harness: `bns-stress` run with DoH primary; verify QPS doesn't
   collapse, no IO-error storms, `bns_doh_*` metrics populate as
   expected.

## 11. Alternatives considered, not chosen

### 11.1 Sidecar pattern (`cloudflared` / `dnscrypt-proxy`)

Standard Pi-hole answer: run a separate DoH-client process on
`127.0.0.1:5053`, point BNS's UDP upstream at it.

Pros: zero BNS code, mature audited sidecar, DoT/DoH3/dnscrypt
"supported" by swapping the sidecar.

Rejected because BNS's design goal is the single static binary on
Pi-class hardware. Sidecar fragments the deploy posture, adds a second
process to monitor and restart, splits observability. Native DoH at
~200 LOC of straightforward stdlib `net/http` is a fair trade for
preserving the single-binary deploy.

### 11.2 DNS Stamps (`sdns://...`)

dnscrypt-proxy's config format: a single opaque base64-encoded string
encoding URL + IP + SNI + cert hash.

Pros: misconfiguration-resistant (can't typo SNI wrong), cert pinning
baked in.

Rejected because the format is opaque to operators. Readable YAML beats
clever encoding. dnscrypt-proxy gets away with it because stamps are the
product's identity; for BNS they would be a foreign concept.

### 11.3 DDR / Designated Resolver auto-discovery (RFC 9462)

UDP upstream advertises its DoH endpoint via SVCB at
`_dns.resolver.arpa`; client auto-upgrades. Operator only configures
UDP upstream.

Pros: zero-config DoH adoption; resolver-published canonical IPs.

Rejected for this spec because RFC 9462 is recent (2023) with sparse
client/server support, adds protocol complexity, and DDR query failures
are hard to debug. Tracked as a potential future direction.

### 11.4 AdGuard `dnsproxy` library

`github.com/AdguardTeam/dnsproxy/upstream` provides DoH, DoT, DoQ, DoH3,
DNS Stamps, and bootstrap handling in one package.

Pros: battle-tested; covers all encrypted transports if/when we want
them.

Rejected because adopting it pulls ~10 transitive deps including
`quic-go`, and the abstraction would replace the existing `Upstream`
interface with AdGuard's own. Overkill for a DoH-only feature.

### 11.5 IP-literal URL form with `tls_servername` override

Earlier iteration of the config schema permitted
`url: https://9.9.9.9/dns-query` + `tls_servername: dns.quad9.net`.

Dropped in favour of the hostname-URL form because every real-world DoH
provider's certificate carries hostname SANs as canonical names. Operators
choosing the IP-literal form gain nothing except verbosity. If the
hypothetical IP-SAN-only-cert use case ever materialises,
`tls_servername` can be added back as a small additive change.

### 11.6 Pinned blocklist endpoint IPs per source

A consistency play with DoH `endpoint_ips`: each
`blocklists.sources[type=http]` entry would carry its own
`endpoint_ips`, eliminating the `BootstrapResolver` and the "DoH IPs
also serve `:53`" assumption.

Rejected outright (not just deferred). Blocklist URLs are almost always
served from CDNs whose IP ranges drift over time (GitHub raw / Fastly
for hagezi, Cloudflare for others). Per-source IP pinning would rot
silently and require constant operator maintenance. The bootstrap
mechanism needs to remain dynamic — operator burden of looking up CDN
IPs once and re-looking-them-up forever is not a trade worth making.
If the current bootstrap aggregation becomes a maintenance burden, a
top-level `bootstrap_addrs:` list decoupled from upstream config is the
better direction (tracked in `docs/TODO.md`).

### 11.7 Recursive blocklist hostname resolution through BNS's own chain

After BNS is up and serving, blocklist hostname resolution could route
through the resolver chain instead of via a parallel `BootstrapResolver`.

Rejected because it couples the blocklist subsystem to chain readiness,
adds a latency interlock (chain busy → blocked fetch → blocked SIGHUP
reload), and is conceptually circular even when not literally deadlocked.
The current dedicated `BootstrapResolver` decouples the two cleanly.

## 12. Risks

- **`endpoint_ips` drift.** Operators paste IPs once and forget them.
  Cloudflare, Google, Quad9 IPs are extremely stable (effectively
  permanent), but a niche provider could change. Mitigation: documented
  expectation that the IPs are operator-maintained alongside the URL.
  TLS cert validation against URL hostname provides a strong sanity
  check: stale IPs that point at a different operator's host fail
  handshake loudly rather than silently routing queries elsewhere.

- **First-query latency under cold connection.** First DoH query after
  startup, or after `IdleConnTimeout`, eats TCP + TLS 1.3 handshake
  cost. Mitigation: existing `warmupProbe` pre-warms the primary
  upstream during startup; long `IdleConnTimeout` (90s) absorbs typical
  idle periods.

- **`bns_upstream_*` label change breaks pre-existing alerts.** Any
  alert that label-matches without `protocol=` continues to work; any
  alert grouping by all labels and asserting cardinality will need
  updates. Mitigation: release notes.

- **Pi 5 TLS handshake CPU cost.** Negligible per research. Cortex-A76
  + ARMv8 Crypto Extensions handle AES-GCM at multi-Gbps; ECDHE
  (X25519) is sub-ms on A76. At the few-thousand-QPS target with
  connection reuse, handshakes amortise to near-zero per-query cost.

- **`:53` assumption for blocklist bootstrap from DoH IPs.** As above:
  holds for every major public provider, fails for self-hosted
  DoH-only. Documented, tracked in `docs/TODO.md`.

- **Provider-side rate limiting on free DoH endpoints.** Cloudflare,
  Google, Quad9 are explicit about reasonable use; BNS's coalescing +
  cache mean per-BNS-instance QPS to upstream stays modest. Not a
  practical concern for home / small-LAN deployment.

## 13. Open questions

None blocking implementation. Items the implementation plan should
re-examine:

- The exact `http2` push-disable wiring. Stdlib `http2.ConfigureTransport`
  returns the `*http2.Transport`; setting `MaxHeaderListSize` and
  disabling `PushHandler` via the transport's settings frame is the
  canonical path. Confirm with current `golang.org/x/net/http2` API in
  the implementation plan; the audit's "MUST establish pushed URI is
  usable" is satisfied by refusing all push.

- Whether to expose `IdleConnTimeout` as a per-upstream config field for
  operator tuning, or leave hardcoded at 90s. Probably hardcoded for now
  (YAGNI); revisit if measured to matter.

- Whether to add a `bns_doh_request_size_bytes` / `_response_size_bytes`
  histogram. Out of this spec; tracked under monitoring follow-ups if
  the existing duration histogram proves insufficient.
