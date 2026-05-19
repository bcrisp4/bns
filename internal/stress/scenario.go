package stress

import (
	"io"
	"time"

	"github.com/tantalor93/dnspyre/v3/pkg/dnsbench"
)

// Scenario describes a single named stress workload. The Build closure
// is the only place a scenario customises the dnspyre Benchmark; the
// orchestrator fills in fields that are universal (Writer, ErrWriter,
// Silent, ProgressBar) afterwards.
type Scenario struct {
	Name          string
	BlocklistPath string            // optional override; empty = orchestrator default
	BNSEnv        map[string]string // additional BNS_* env vars
	Build         func(target string, dur time.Duration, c uint32) dnsbench.Benchmark
}

// scenarios is the in-memory registry. Populated by sibling packages
// (e.g. scenarios.NewMixed in scenarios package init).
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
func stdSilent(b *dnsbench.Benchmark) {
	b.Writer = io.Discard
	b.Silent = true
	b.ProgressBar = false
	b.JSON = false
}
