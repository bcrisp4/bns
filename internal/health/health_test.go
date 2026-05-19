package health_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bcrisp4/bns/internal/health"
	"github.com/stretchr/testify/require"
)

func TestLiveness_AlwaysOK(t *testing.T) {
	r := health.NewReadiness()
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.LivenessHandler())
	mux.Handle("/readyz", health.ReadinessHandler(r))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestReadiness_StartsNotReady(t *testing.T) {
	r := health.NewReadiness()
	mux := http.NewServeMux()
	mux.Handle("/readyz", health.ReadinessHandler(r))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestReadiness_AllChecksReadyServes200(t *testing.T) {
	r := health.NewReadiness()
	mux := http.NewServeMux()
	mux.Handle("/readyz", health.ReadinessHandler(r))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r.SetBlocklistReady(true)
	r.SetListenersReady(true)
	r.SetUpstreamReady(true)

	resp, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
