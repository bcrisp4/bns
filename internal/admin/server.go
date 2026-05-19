// Package admin hosts the BNS administrative HTTP endpoints:
// /metrics (Prometheus), /healthz (liveness), /readyz (readiness).
package admin

import (
	"context"
	"net"
	"net/http"

	"github.com/bcrisp4/bns/internal/health"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server bundles the admin HTTP server with its mux and listener.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// New builds an admin server bound to ln, exposing metrics from reg and
// health from rdy.
func New(ln net.Listener, reg prometheus.Gatherer, rdy *health.Readiness) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler(rdy))

	return &Server{
		http: &http.Server{Handler: mux},
		ln:   ln,
	}
}

// Serve blocks until the server stops; returns the error from http.Serve.
// http.ErrServerClosed is the normal shutdown signal.
func (s *Server) Serve() error {
	return s.http.Serve(s.ln)
}

// Shutdown stops accepting connections and waits for in-flight requests
// to complete (bounded by ctx).
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
