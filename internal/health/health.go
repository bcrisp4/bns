// Package health implements /healthz (liveness) and /readyz (readiness) probes.
package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// Readiness aggregates the three readiness sub-checks.
type Readiness struct {
	blocklist atomic.Bool
	listeners atomic.Bool
	upstream  atomic.Bool
}

// NewReadiness returns a Readiness with all sub-checks initially false.
func NewReadiness() *Readiness { return &Readiness{} }

// SetBlocklistReady marks the blocklist sub-check ready (true/false).
func (r *Readiness) SetBlocklistReady(v bool) { r.blocklist.Store(v) }

// SetListenersReady marks the DNS listeners sub-check ready (true/false).
func (r *Readiness) SetListenersReady(v bool) { r.listeners.Store(v) }

// SetUpstreamReady marks the upstream warmup sub-check ready (true/false).
func (r *Readiness) SetUpstreamReady(v bool) { r.upstream.Store(v) }

// Ready returns true iff all sub-checks are ready.
func (r *Readiness) Ready() bool {
	return r.blocklist.Load() && r.listeners.Load() && r.upstream.Load()
}

// LivenessHandler always returns 200 OK.
func LivenessHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// ReadinessHandler returns 200 iff r.Ready(), else 503 with a JSON body
// reporting which sub-checks failed.
func ReadinessHandler(r *Readiness) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]bool{
			"blocklist": r.blocklist.Load(),
			"listeners": r.listeners.Load(),
			"upstream":  r.upstream.Load(),
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Ready() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(body)
	})
}
