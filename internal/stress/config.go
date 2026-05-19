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
