// Package admin hosts the BNS administrative HTTP endpoints:
// /metrics (Prometheus), /healthz (liveness), /readyz (readiness),
// and optionally /debug/pprof/* when WithPprof is passed.
package admin

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"

	"github.com/bcrisp4/bns/internal/health"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server bundles the admin HTTP server with its mux and listener.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// Option mutates the admin mux at construction time.
type Option func(mux *http.ServeMux)

// WithPprof mounts net/http/pprof handlers under /debug/pprof/. Off by
// default; the BNS serve command opts in via the --pprof flag.
func WithPprof() Option {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
}

// New builds an admin server bound to ln, exposing metrics from reg and
// health from rdy.
func New(ln net.Listener, reg prometheus.Gatherer, rdy *health.Readiness, opts ...Option) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler(rdy))
	for _, o := range opts {
		o(mux)
	}
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
