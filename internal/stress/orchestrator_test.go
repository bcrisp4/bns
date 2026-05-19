package stress_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	"github.com/stretchr/testify/require"
)

func TestAdminBaseURL_PrefixesScheme(t *testing.T) {
	require.Equal(t, "http://127.0.0.1:9090", stress.AdminBaseURL("127.0.0.1:9090"))
	require.Equal(t, "http://pi.example:9090", stress.AdminBaseURL("pi.example:9090"))
}

func TestWaitForReady_SuccessAfterPolling(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if hits.Add(1) < 3 {
			http.Error(w, "warming up", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, stress.WaitForReady(ctx, srv.URL, 25*time.Millisecond))
	require.GreaterOrEqual(t, hits.Load(), int32(3))
}

func TestWaitForReady_FailsOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := stress.WaitForReady(ctx, srv.URL, 25*time.Millisecond)
	require.Error(t, err)
}

func TestBNSEnv_MergesScenarioAndDefaults(t *testing.T) {
	env := stress.BNSEnv(map[string]string{
		"BNS_LOGGING__LEVEL":               "warn",
		"BNS_BLOCKLISTS__SOURCES__0__PATH": "/tmp/list.txt",
	}, map[string]string{
		"BNS_CACHE__CAPACITY": "10000",
	})

	require.Contains(t, env, "BNS_LOGGING__LEVEL=warn")
	require.Contains(t, env, "BNS_CACHE__CAPACITY=10000")
	require.Contains(t, env, "BNS_BLOCKLISTS__SOURCES__0__PATH=/tmp/list.txt")
}
