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
				Server:      target,
				Types:       []string{"A", "AAAA"},
				Concurrency: c,
				Duration:    dur,
				Probability: 0.7,
				Queries:     []string{"@scripts/stress/queries/mixed.txt"},
				Recurse:     true,
				Rcodes:      true,
				HistDisplay: false,
				HistPre:     dnsbench.DefaultHistPrecision,
				HistMin:     0,
				HistMax:     dnsbench.DefaultRequestTimeout,
				Writer:      io.Discard,
				Silent:      true,
				ProgressBar: false,
				JSON:        false,
			}
		},
	}
}

func init() {
	stress.RegisterScenario(NewMixed())
}
