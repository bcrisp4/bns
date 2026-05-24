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
