# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

BNS (Ben's Name Server) — caching DNS forwarder with ad-blocking for a small private network. Pi-hole-like. Go, single-binary, deployable on a Raspberry Pi.

Module: `github.com/bcrisp4/bns`. Go 1.26.

## Status

Greenfield. `cmd/` is empty, no packages yet, no tests yet. Treat the seed prompt as authoritative spec.

## Tech stack — non-obvious

- **DNS library: `codeberg.org/miekg/dns` v2.** NOT `github.com/miekg/dns` (v1, unmaintained). The v2 API is incompatible with v1. Do not import the GitHub path. When fetching docs, use Context7 / codeberg, not the stale GH v1 docs.
- Metrics: Prometheus (`/metrics`).
- Logging: stdlib `slog`, structured, stdout/stderr.
- CLI: `cobra`. Config: `viper` (flags + env + YAML, in that precedence).
- Health: `/healthz` (liveness), `/readyz` (readiness).

## Architecture invariants

These come from the spec; preserve them when designing:

- **Two listeners**: UDP and TCP, same handler logic. Forward protocol pluggable — DoH/DoT/DoQ are future work but the forwarder interface should not preclude them.
- **Blocklist matching** is exact + subdomain wildcard. `ads.com` blocks `ads.com` and `*.ads.com`. Must scale to ~100K entries.
- **Cache** is in-memory, bounded, TTL-aware, with eviction when full. Negative caching supported. Not persistent across restarts. Capacity configurable at runtime.
- **Upstream dedup** via `singleflight` to coalesce concurrent identical queries.
- **Thread-safety required everywhere** — listeners, cache, blocklist, metrics all hit concurrently.
- **Query logging off by default**; gated behind config.

## Performance posture

Target: a few thousand QPS on Raspberry Pi-class hardware. NOT high-scale.

- Be allocation-conscious on the hot path (query handle → cache lookup → forward → respond). Reuse buffers where the library allows.
- Mind GC pauses — large blocklist structures should be allocated once at load, not churned.
- **Do not pre-optimize.** YAGNI/KISS. Profile with `pprof` first; only optimize when a measurement justifies it.

## Development practices (enforced by user)

- **TDD, red-green-refactor.** Write the failing test first.
- Idiomatic Go. Boring, well-trodden patterns over clever ones.
- Document public *and* internal interfaces. Extra care on load-bearing or non-obvious logic — explain *why*.
- Designs/specs belong in `docs/specs` and get committed.
- Implementation plans belong in `docs/plans` and get committed.
- Build binaries to `bin/` (gitignored). Example: `go build -o bin/bns ./cmd/bns`.
