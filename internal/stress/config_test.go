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
