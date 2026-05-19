# BNS (Ben's Name Server) Initial Seed Prompt

This is an intial prompt to kickstart the development of BNS (Ben's Name Server).

## Overview

BNS is a caching DNS forwarder with ad-blocking. It is written in Go. It is similar in nature to tools like Pi-hole. It's purpose is to provide network-wide ad blocking at the DNS level for a small private network - Ben's personal network.

## Required Functionality

1. Must accept and respond to DNS queries using the standard DNS protocol over UDP and TCP.
2. Must forward DNS queries to upstream DNS servers using the standard DNS protocol over UDP and TCP.
3. Should be extendable to support forwarding queries with additional DNS protocols such as DNS over HTTPS, DNS over TLS, and DNS over QUIC in the future - this is not a requirement for the initial version.
4. Must blackhole DNS queries for known ad domains using blocklists such as github.com/hagezi/dns-blocklists
5. Must support matching subdomains against a list of known ad domains. e.g. if `ads.com` is in the list, `ads.com` and `*.ads.com` should be blocked.
6. Must cache DNS query responses in memory to improve performance and reduce load on upstream DNS servers. The cache should respect TTL values returned by upstream DNS servers and expire cached responses after their TTL expires.
7. Should support negative caching to avoid unnecessary upstream DNS queries for known non-existent domains.
8. Should minimise requests to upstream DNS servers using tools such as singleflight to avoid redundant queries.
9. Must expose a Prometheus metrics endpoint for monitoring. Metrics must include (not limited to):
  - Performance of downstream DNS servers: query count, query duration, query success rate, query error rate
  - Performance of the BNS server itself: query count, query duration, query success rate, query error rate
  - Internal BNS metrics: cache hit rate, cache size, memory usage, CPU usage
10. Must use structured logging using the standard slog package.
11. Must support query logging for debugging, but this should be disabled by default.
12. Must be fully configurable via CLI flags, environment variables, and a YAML config file.
13. The cache must be bounded and use a suitable eviction strategy when the cache reaches its capacity.
14. Must support concurrent access and be thread-safe.
15. Must log to stdout and stderr by default.
16. Must provide readiness and liveness probes (e.g. for Kubernetes).
17. The maximum size of the cache must be configurable at runtime.
18. The DNS forwarders used for resolving external DNS queries must be configurable at runtime.
19. Must support large DNS blocklists efficiently. O(100K) entries.
20. The listen address must be configurable at runtime.

### Non-requirements

1. The cache DOES NOT need to be persistent across restarts.

## Performance Considerations

BNS is intended to be used in a small private network. It does not need to handle high traffic or scale to millions of DNS queries. It is only expected to handle a few thousand queries per second.

BNS is intended to be deployed on a single machine like a Raspberry Pi. You should be conscious of the memory and CPU usage of BNS, as hardware resources may be limited.

Be mindful of allocating in the hot path. This should be done sparingly and only when necessary.

Consider issues with GC pauses, how they can impact performance and how to mitigate them.

## Tech Stack / Dependencies

Language: Go
DNS library: miekg/dns from https://codeberg.org/miekg/dns - IMPORTANT: this is a v2 of the library that is no longer hosted on GitHub. The original v1 is hosted on GitHub at https://github.com/miekg/dns but is no longer maintained. v2 has a different API and is not compatible with v1.
Metrics: Prometheus
Logging: slog
CLI: cobra
Config: viper

## Software Development Practices

1. You must follow a test-driven development (TDD) methodology with red-green-refactor cycles.
2. You must write clean and idiomatic Go code.
3. You must not not optimize prematurely. YAGNI! KISS! `pprof` first and only optimize when necessary.
4. You must clearly document all public and internal interfaces and functions.
5. You must pay especially close attention to documentation for all load-bearing code and any non-obvious or complex logic.

## MVP Success Criteria

1. I can run BNS locally on a single machine without any issues.
2. I can send DNS queries to BNS and receive responses without any issues.
3. I can enable query logging and the queries are logged to stdout correctly.
4. I can scrape performance metrics from BNS using Prometheus at `/metrics`.
5. I can configure BNS via CLI flags, environment variables, and a YAML config file.
6. I can access liveness and readiness probes at `/healthz` and `/readyz` respectively.
7. I can try a DNS query for a know advertisement domain and the response is blocked as expected.
8. I can see entries in the DNS cache and the cache is updated as expected. A second request for the same domain should return from the cache without any delay and should be measurably faster than the first request.
