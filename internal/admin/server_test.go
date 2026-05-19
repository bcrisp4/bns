package admin_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/admin"
	"github.com/bcrisp4/bns/internal/health"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestAdminServer_ServesAllEndpoints(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()

	reg := prometheus.NewRegistry()
	r := health.NewReadiness()
	r.SetBlocklistReady(true)
	r.SetListenersReady(true)
	r.SetUpstreamReady(true)

	srv := admin.New(ln, reg, r)
	go func() { _ = srv.Serve() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	wait(t, "http://"+addr+"/healthz")

	for path, want := range map[string]int{
		"/healthz": 200,
		"/readyz":  200,
		"/metrics": 200,
	} {
		resp, err := http.Get("http://" + addr + path)
		require.NoError(t, err, path)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		require.Equal(t, want, resp.StatusCode, path)
	}
}

func wait(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server never came up at %s", url)
}
