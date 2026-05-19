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
